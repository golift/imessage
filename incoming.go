package imessage

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

/*
   CREATE TABLE message (
     ROWID INTEGER PRIMARY KEY AUTOINCREMENT,
     guid TEXT UNIQUE NOT NULL,
     text TEXT,
     replace INTEGER DEFAULT 0,
     service_center TEXT,
     handle_id INTEGER DEFAULT 0,
     subject TEXT,
     country TEXT,
     attributedBody BLOB,
     version INTEGER DEFAULT 0,
     type INTEGER DEFAULT 0,
     service TEXT, account TEXT,
     account_guid TEXT,
     error INTEGER DEFAULT 0,
     date INTEGER, date_read INTEGER,
     date_delivered INTEGER,
     is_delivered INTEGER DEFAULT 0,
     is_finished INTEGER DEFAULT 0,
     is_emote INTEGER DEFAULT 0,
     is_from_me INTEGER DEFAULT 0,
     is_empty INTEGER DEFAULT 0,
     is_delayed INTEGER DEFAULT 0,
     is_auto_reply INTEGER DEFAULT 0,
     is_prepared INTEGER DEFAULT 0,
     is_read INTEGER DEFAULT 0,
     is_system_message INTEGER DEFAULT 0,
     is_sent INTEGER DEFAULT 0,
     has_dd_results INTEGER DEFAULT 0,
     is_service_message INTEGER DEFAULT 0,
     is_forward INTEGER DEFAULT 0,
     was_downgraded INTEGER DEFAULT 0,
     is_archive INTEGER DEFAULT 0,
     cache_has_attachments INTEGER DEFAULT 0,
     cache_roomnames TEXT,
     was_data_detected INTEGER DEFAULT 0,
     was_deduplicated INTEGER DEFAULT 0,
     is_audio_message INTEGER DEFAULT 0,
     is_played INTEGER DEFAULT 0,
     date_played INTEGER,
     item_type INTEGER DEFAULT 0,
     other_handle INTEGER DEFAULT 0,
     group_title TEXT,
     group_action_type INTEGER DEFAULT 0,
     share_status INTEGER DEFAULT 0,
     share_direction INTEGER DEFAULT 0,
     is_expirable INTEGER DEFAULT 0,
     expire_state INTEGER DEFAULT 0,
     message_action_type INTEGER DEFAULT 0,
     message_source INTEGER DEFAULT 0,
     associated_message_guid TEXT,
     associated_message_type INTEGER DEFAULT 0,
     balloon_bundle_id TEXT,
     payload_data BLOB,
     expressive_send_style_id TEXT,
     associated_message_range_location INTEGER DEFAULT 0,
     associated_message_range_length INTEGER DEFAULT 0,
     time_expressive_send_played INTEGER,
     message_summary_info BLOB,
     ck_sync_state INTEGER DEFAULT 0,
     ck_record_id TEXT DEFAULT NULL,
     ck_record_change_tag TEXT DEFAULT NULL,
     destination_caller_id TEXT DEFAULT NULL,
     sr_ck_sync_state INTEGER DEFAULT 0,
     sr_ck_record_id TEXT DEFAULT NULL,
     sr_ck_record_change_tag TEXT DEFAULT NULL
   ); */

// IncomingChan connects a channel to a matched string in a message.
// Similar to the IncomingCall method, this will send an incming message
// to a channel. Any message with text matching `match` is sent.
// Use '*' for all messages. Use "" for files and empty messages.
func (c *Config) IncomingChan(match string, channel chan Incoming) {
	c.chanBinds = append(c.chanBinds, &chanBinding{Match: match, Chan: channel})
}

// IncomingCall connects a callback function to a matched string in a message.
// This methods creates a callback that is run in a go routine any time
// a message containing `match` is found
// Use '*' for all messages. Use "" for files and empty messages.
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
		if (bind.Match == "*" && m.Text != "") || strings.Contains(m.Text, bind.Match) {
			go bind.Func(*m)
		}
	}
}

func (m *Incoming) mesgChans(chans []*chanBinding) {
	for _, bind := range chans {
		if (bind.Match == "*" && m.Text != "") || strings.Contains(m.Text, bind.Match) {
			bind.Chan <- *m
		}
	}
}
