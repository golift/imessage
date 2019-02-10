package imessage

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Incoming is represents a message from someone. This struct is filled out
// and sent to incoming callback methods and/or to bound channels.
type Incoming struct {
	RowID int64  // RowID is the unique database row id.
	From  string // From is the handle of the user who sent the message.
	Text  string // Text is the body of the message.
	File  bool   // File is true if a file is attached. (no way to access it atm)
}

type chanBinding struct {
	Match string
	Chan  chan Incoming
}

type funcBinding struct {
	Match string
	Func  func(msg Incoming)
}

// IncomingChan connects a channel to a matched string in a message.
// Similar to the IncomingCall method, this will send an incoming message
// to a channel. Any message with text matching `match` is sent. Regexp supported.
// Use '.*' for all messages. The channel blocks, so avoid long operations.
func (m *Messages) IncomingChan(match string, channel chan Incoming) {
	m.chanLock.Lock()
	defer m.chanLock.Unlock()
	m.chanBinds = append(m.chanBinds, &chanBinding{Match: match, Chan: channel})
}

// IncomingCall connects a callback function to a matched string in a message.
// This methods creates a callback that is run in a go routine any time
// a message containing `match` is found. Use '.*' for all messages. Supports regexp.
func (m *Messages) IncomingCall(match string, callback func(msg Incoming)) {
	m.funcLock.Lock()
	defer m.funcLock.Unlock()
	m.funcBinds = append(m.funcBinds, &funcBinding{Match: match, Func: callback})
}

// RemoveChan deletes a message match to channel made with IncomingChan()
func (m *Messages) RemoveChan(match string) int {
	m.chanLock.Lock()
	defer m.chanLock.Unlock()
	removed := 0
	for i, rlen := 0, len(m.chanBinds); i < rlen; i++ {
		j := i - removed
		if m.chanBinds[j].Match == match {
			m.chanBinds = append(m.chanBinds[:j], m.chanBinds[j+1:]...)
			removed++
		}
	}
	return removed
}

// RemoveCall deletes a message match to function callback made with IncomingCall()
func (m *Messages) RemoveCall(match string) int {
	m.funcLock.Lock()
	defer m.funcLock.Unlock()
	removed := 0
	for i, rlen := 0, len(m.chanBinds); i < rlen; i++ {
		j := i - removed
		if m.funcBinds[j].Match == match {
			m.funcBinds = append(m.funcBinds[:j], m.funcBinds[j+1:]...)
			removed++
		}
	}
	return removed
}

// processIncomingMessages starts the iMessage-sqlite3 db watcher routine(s).
func (m *Messages) processIncomingMessages() {
	stopDB := make(chan bool)
	go func() {
		for {
			select {
			case msg := <-m.inChan:
				m.callBacks(msg)
				m.mesgChans(msg)
			case <-m.stopIncoming:
				stopDB <- true
				return
			}
		}
	}()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		_ = m.checkErr(err, "fsnotify failed, polling instead")
		go m.pollSQL(stopDB)
		return
	}
	go m.fsnotifySQL(watcher, stopDB)
	if err = watcher.Add(filepath.Dir(m.config.SQLPath)); err != nil {
		_ = m.checkErr(err, "fsnotify watcher failed")
	}
}

func (m *Messages) fsnotifySQL(watcher *fsnotify.Watcher, stopDB chan bool) {
	var last time.Time
	defer func() {
		_ = watcher.Close()
	}()
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				m.eLogf("fsnotify watcher failed. incoming message routines stopped")
				m.Stop()
			}
			if event.Op&fsnotify.Write == fsnotify.Write &&
				last.Add(m.config.Interval).Before(time.Now()) {
				m.dLogf("modified file: %v", event.Name)
				last = time.Now()
				m.checkForNewMessages()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				m.eLogf("fsnotify watcher errors failed. incoming message routines stopped.")
				m.Stop()
			}
			_ = m.checkErr(err, "fsnotify watcher")
		case <-stopDB:
			return
		}
	}
}

func (m *Messages) pollSQL(stopDB chan bool) {
	ticker := time.NewTicker(m.config.Interval)
	for {
		select {
		case <-stopDB:
			return
		case <-ticker.C:
			m.checkForNewMessages()
		}
	}
}

func (m *Messages) checkForNewMessages() {
	if m.getDB() != nil {
		return
	}
	defer m.closeDB()
	sql := `SELECT message.rowid as rowid, handle.id as handle, cache_has_attachments, message.text as text ` +
		`FROM message INNER JOIN handle ON message.handle_id = handle.ROWID ` +
		`WHERE is_from_me=0 AND message.rowid > $id ORDER BY message.date ASC`
	query := m.db.Prep(sql)
	query.SetInt64("$id", m.startID)
	defer func() {
		_ = m.checkErr(query.Finalize(), sql)
	}()
	for {
		if hasRow, err := query.Step(); err != nil {
			_ = m.checkErr(err, sql)
			return
		} else if !hasRow {
			return
		}
		m.startID = query.GetInt64("rowid")
		msg := Incoming{
			RowID: m.startID,
			From:  strings.TrimSpace(query.GetText("handle")),
			Text:  strings.TrimSpace(query.GetText("text")),
		}
		if query.GetInt64("cache_has_attachments") == 1 {
			msg.File = true
		}
		m.inChan <- msg
		m.dLogf("new message id %d from: %s size: %d", msg.RowID, msg.From, len(msg.Text))
	}
}

func (m *Messages) getCurrentID() error {
	sql := `SELECT MAX(rowid) AS id FROM message`
	if err := m.getDB(); err != nil {
		return err
	}
	defer m.closeDB()
	query := m.db.Prep(sql)
	defer func() {
		_ = m.checkErr(query.Finalize(), sql)
	}()
	m.dLogf("querying current id")
	hasrow, err := query.Step()
	_ = m.checkErr(err, sql)
	if hasrow && err == nil {
		m.startID = query.GetInt64("id")
		return nil
	}
	return errors.New("no message rows found")
}

func (m *Messages) callBacks(msg Incoming) {
	m.funcLock.RLock()
	defer m.funcLock.RUnlock()
	for _, bind := range m.funcBinds {
		matched, err := regexp.MatchString(bind.Match, msg.Text)
		if err = m.checkErr(err, bind.Match); err == nil && matched {
			go bind.Func(msg)
		}
	}
}

func (m *Messages) mesgChans(msg Incoming) {
	m.chanLock.RLock()
	defer m.chanLock.RUnlock()
	for _, bind := range m.chanBinds {
		matched, err := regexp.MatchString(bind.Match, msg.Text)
		if err = m.checkErr(err, bind.Match); err == nil && matched {
			bind.Chan <- msg
		}
	}
}
