// Package imessage is used to interact with iMessage (Messages.app) on macOS
//
// Use this library to send and receive messages using iMessage.
// Can be used to make a chat bot or something similar. You can bind either a
// function or a channel to any or all messages. The Send() method uses
// AppleScript, which is likely going to require some tinkering. You got this
// far, so I trust you'll figure that out. Let me know how it works out.
//
// The library uses `fsnotify` to poll for db updates, then checks the database for changes.
// Only new messages are processed. If somehow `fsnotify` fails it will fall back to polling
// the database. Pay attention to the debug/error logs.
package imessage

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"

	"crawshaw.io/sqlite"
)

// Config is our input data, data store, and interface to methods.
// Fill out this struct and pass it into imessage.Init()
type Config struct {
	// ClearMsgs will cause this library to clear all iMessage conversations.
	ClearMsgs bool `xml:"clear_messages" json:"clear_messages,_omitempty" toml:"clear_messages,_omitempty" yaml:"clear_messages"`
	// This is the channel buffer size.
	QueueSize int `xml:"queue_size" json:"queue_size,_omitempty" toml:"queue_size,_omitempty" yaml:"queue_size"`
	// How many applescript retries to perform.
	Retries int `xml:"retries" json:"retries,_omitempty" toml:"retries,_omitempty" yaml:"retries"`
	// Timeout in seconds for AppleScript Exec commands.
	Timeout int `xml:"timeout" json:"timeout,_omitempty" toml:"timeout,_omitempty" yaml:"timeout"`
	// SQLPath is the location if the iMessage database.
	SQLPath string `xml:"sql_path" json:"sql_path,_omitempty" toml:"sql_path,_omitempty" yaml:"sql_path"`
	// Loggers.
	ErrorLog Logger `xml:"-" json:"-" toml:"-" yaml:"-"`
	DebugLog Logger `xml:"-" json:"-" toml:"-" yaml:"-"`
}

// Messages is the interface into this module. Init() returns this struct.
// All of the important library methods are bound to this type.
// ErrorLog and DebugLog can be set directly, or use the included methods to set them.
type Messages struct {
	*Config                 // Input config.
	running   bool          // Only used in Start() and Stop()
	currentID int64         // Constantly growing
	outChan   chan Outgoing // send
	inChan    chan Incoming // recieve
	binds                   // incoming message handlers
}

// Logger is a base interface to deal with changing log outs.
// Pass a matching interface (like log.Printf) to capture
// messages from the running background go routines.
type Logger interface {
	Print(v ...interface{})
	Printf(fmt string, v ...interface{})
	Println(v ...interface{})
}

// Init is the primary function to retrieve a Message handler.
// Pass a Config struct in and use the returned Messages struct to send
// and respond to incoming messages.
func Init(config *Config) (*Messages, error) {
	if _, err := os.Stat(config.SQLPath); err != nil {
		return nil, fmt.Errorf("sql file access error: %v", err)
	}
	config.setDefaults()
	m := &Messages{
		Config:  config,
		outChan: make(chan Outgoing, config.QueueSize),
		inChan:  make(chan Incoming, config.QueueSize),
	}
	// Try to open, query and close the database.
	return m, m.getCurrentID()
}

func (c *Config) setDefaults() {
	if c.Retries == 0 {
		c.Retries = 3
	} else if c.Retries > 10 {
		c.Retries = 10
	}
	if c.QueueSize < 10 {
		c.QueueSize = 10
	}
	if c.Timeout < 10 {
		c.Timeout = 10
	}
	if c.ErrorLog == nil {
		c.ErrorLog = log.New(ioutil.Discard, "[ERROR] ", log.LstdFlags)
	}
	if c.DebugLog == nil {
		c.DebugLog = log.New(ioutil.Discard, "[DEBUG] ", log.LstdFlags)
	}
}

// Start starts the iMessage-sqlite3 db and outgoing message watcher routine(s).
// Outgoing messages wont work and incoming message are ignored until Start() runs.
func (m *Messages) Start() error {
	if m.running {
		return errors.New("already running")
	} else if err := m.getCurrentID(); err != nil {
		return err
	}
	m.running = true
	m.DebugLog.Printf("starting with id %d", m.currentID)
	go m.processOutgoingMessages()
	return m.processIncomingMessages()
}

// Stop cancels the iMessage-sqlite3 db and outgoing message watcher routine(s).
// Outgoing messages stop working when the routines are stopped.
// Incoming messages are ignored after this runs.
func (m *Messages) Stop() {
	defer func() { m.running = false }()
	if m.running {
		close(m.inChan)
		close(m.outChan)
	}
}

// getDB opens a database connection and locks access, so only one reader can
// access the db at once.
func (m *Messages) getDB() (*sqlite.Conn, error) {
	m.Lock()
	m.DebugLog.Println("opening database:", m.SQLPath)
	db, err := sqlite.OpenConn(m.SQLPath, sqlite.SQLITE_OPEN_READONLY)
	m.checkErr(err, "opening database")
	return db, err
}

// closeDB stops reading the sqlite db and unlocks the read lock.
func (m *Messages) closeDB(db io.Closer) {
	m.DebugLog.Println("closing database:", m.SQLPath)
	if db == nil {
		m.DebugLog.Print("db was nil? not closed")
		return
	}
	defer m.Unlock()
	m.checkErr(db.Close(), "closing database: "+m.SQLPath)
}

// checkErr writes an error to Logger if it exists.
func (m *Messages) checkErr(err error, msg string) {
	if err != nil {
		m.ErrorLog.Printf("%s: %q\n", msg, err)
	}
}
