package imessage

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Incoming is represents a message from someone.
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
	Func  func(Incoming)
}

// IncomingChan connects a channel to a matched string in a message.
// Similar to the IncomingCall method, this will send an incming message
// to a channel. Any message with text matching `match` is sent.
// Use '.*' for all messages. The channel blocks, so avoid long operations.
func (c *Config) IncomingChan(match string, channel chan Incoming) {
	c.chanBinds = append(c.chanBinds, &chanBinding{Match: match, Chan: channel})
}

// IncomingCall connects a callback function to a matched string in a message.
// This methods creates a callback that is run in a go routine any time
// a message containing `match` is found. Use '.*' for all messages.
func (c *Config) IncomingCall(match string, callback func(Incoming)) {
	c.funcBinds = append(c.funcBinds, &funcBinding{Match: match, Func: callback})
}

// RemoveChan deletes a message match to channel made with IncomingChan()
func (c *Config) RemoveChan(match string) int {
	removed := 0
	for i, rlen := 0, len(c.chanBinds); i < rlen; i++ {
		j := i - removed
		if c.chanBinds[j].Match == match {
			c.chanBinds = append(c.chanBinds[:j], c.chanBinds[j+1:]...)
			removed++
		}
	}
	return removed
}

// RemoveCall deletes a message match to function callback made with IncomingCall()
func (c *Config) RemoveCall(match string) int {
	removed := 0
	for i, rlen := 0, len(c.chanBinds); i < rlen; i++ {
		j := i - removed
		if c.funcBinds[j].Match == match {
			c.funcBinds = append(c.funcBinds[:j], c.funcBinds[j+1:]...)
			removed++
		}
	}
	return removed
}

// processIncomingMessages starts the iMessage-sqlite3 db watcher routine(s).
func (c *Config) processIncomingMessages() {
	stopDB := make(chan bool)
	go func() {
		for {
			select {
			case msg := <-c.inChan:
				msg.callBacks(c.funcBinds)
				msg.mesgChans(c.chanBinds)
			case <-c.stopIncoming:
				stopDB <- true
				return
			}
		}
	}()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		_ = c.checkErr(err, "fsnotify failed, polling instead")
		go c.pollSQL(stopDB)
		return
	}
	go c.fsnotifySQL(watcher, stopDB)
	if err = watcher.Add(filepath.Dir(c.SQLPath)); err != nil {
		_ = c.checkErr(err, "fsnotify watcher failed")
	}
}

func (c *Config) fsnotifySQL(watcher *fsnotify.Watcher, stopDB chan bool) {
	var last time.Time
	defer func() {
		_ = watcher.Close()
	}()
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				c.eLogf("fsnotify watcher failed. incoming message routines stopped")
				c.Stop()
			}
			if event.Op&fsnotify.Write == fsnotify.Write &&
				last.Add(c.Interval).Before(time.Now()) {
				c.dLogf("modified file: %v", event.Name)
				last = time.Now()
				c.checkForNewMessages()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				c.eLogf("fsnotify watcher errors failed. incoming message routines stopped.")
				c.Stop()
			}
			_ = c.checkErr(err, "fsnotify watcher")
		case <-stopDB:
			return
		}
	}
}

func (c *Config) pollSQL(stopDB chan bool) {
	ticker := time.NewTicker(c.Interval)
	for {
		select {
		case <-stopDB:
			return
		case <-ticker.C:
			c.checkForNewMessages()
		}
	}
}

func (c *Config) checkForNewMessages() {
	if c.getDB() != nil {
		return
	}
	defer c.closeDB()
	sql := `SELECT message.rowid as rowid, handle.id as handle, cache_has_attachments, message.text as text ` +
		`FROM message INNER JOIN handle ON message.handle_id = handle.ROWID ` +
		`WHERE is_from_me=0 AND message.rowid > $id ORDER BY message.date ASC`
	query := c.db.Prep(sql)
	query.SetInt64("$id", c.startID)
	defer func() {
		_ = c.checkErr(query.Finalize(), sql)
	}()
	for {
		if hasRow, err := query.Step(); err != nil {
			_ = c.checkErr(err, sql)
			return
		} else if !hasRow {
			return
		}
		c.startID = query.GetInt64("rowid")
		m := Incoming{
			RowID: c.startID,
			From:  strings.TrimSpace(query.GetText("handle")),
			Text:  strings.TrimSpace(query.GetText("text")),
		}
		if query.GetInt64("cache_has_attachments") == 1 {
			m.File = true
		}
		c.inChan <- m
		c.dLogf("new message id %d from: %s size: %d", m.RowID, m.From, len(m.Text))
	}
}

func (c *Config) getCurrentID() error {
	sql := `SELECT MAX(rowid) AS id FROM message`
	if err := c.getDB(); err != nil {
		return err
	}
	defer c.closeDB()
	query := c.db.Prep(sql)
	defer func() {
		_ = c.checkErr(query.Finalize(), sql)
	}()
	c.dLogf("querying current id")
	hasrow, err := query.Step()
	_ = c.checkErr(err, sql)
	if hasrow && err == nil {
		c.startID = query.GetInt64("id")
		return nil
	}
	return errors.New("no message rows found")
}

func (m *Incoming) callBacks(funcs []*funcBinding) {
	for _, bind := range funcs {
		matched, _ := regexp.MatchString(bind.Match, m.Text)
		if matched {
			go bind.Func(*m)
		}
	}
}

func (m *Incoming) mesgChans(chans []*chanBinding) {
	for _, bind := range chans {
		matched, _ := regexp.MatchString(bind.Match, m.Text)
		if matched {
			bind.Chan <- *m
		}
	}
}
