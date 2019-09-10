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
	"io/ioutil"
	"log"
	"sync"
	"time"

	"crawshaw.io/sqlite"
)

// DefaultDuration is the minimum interval that must pass before opening the database again.
var DefaultDuration = 250 * time.Millisecond

// Config is our input data, data store, and interface to methods.
// Fill out this struct and pass it into imessage.Init()
type Config struct {
	ClearMsgs bool     `xml:"clear_messages" json:"clear_messages,_omitempty" toml:"clear_messages,_omitempty" yaml:"clear_messages"`
	QueueSize int      `xml:"queue_size" json:"queue_size,_omitempty" toml:"queue_size,_omitempty" yaml:"queue_size"`
	Retries   int      `xml:"retries" json:"retries,_omitempty" toml:"retries,_omitempty" yaml:"retries"`
	SQLPath   string   `xml:"sql_path" json:"sql_path,_omitempty" toml:"sql_path,_omitempty" yaml:"sql_path"`
	Interval  Duration `xml:"interval" json:"interval,_omitempty" toml:"interval,_omitempty" yaml:"interval"`
	ErrorLog  Logger   `xml:"-" json:"-" toml:"-" yaml:"-"`
	DebugLog  Logger   `xml:"-" json:"-" toml:"-" yaml:"-"`
}

// Duration allows unmarshalling a time value from a config file.
type Duration struct {
	time.Duration
}

// UnmarshalText parses a duration type from a config file.
func (d *Duration) UnmarshalText(data []byte) (err error) {
	d.Duration, err = time.ParseDuration(string(data))
	return
}

// Messages is the interface into this module. Init() returns this struct.
// All of the important library methods are bound to this type.
// ErrorLog and DebugLog can be set directly, or use the included methods to set them.
type Messages struct {
	*Config
	running   bool
	currentID int64
	outChan   chan Outgoing
	inChan    chan Incoming
	stopChan  chan bool
	sync.Mutex
	binds
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
	m := &Messages{
		Config:   config,
		outChan:  make(chan Outgoing, config.QueueSize),
		inChan:   make(chan Incoming, config.QueueSize),
		stopChan: make(chan bool),
	}
	m.setDefaults()
	if m.SQLPath == "" {
		return m, nil
	}
	// Try to open, query and close the database.
	return m, m.getCurrentID()
}

func (m *Messages) setDefaults() {
	if m.Retries == 0 {
		m.Retries = 1
	} else if m.Retries > 10 {
		m.Retries = 10
	}
	if m.Interval.Duration == 0 {
		m.Interval.Duration = DefaultDuration
	} else if m.Interval.Duration > 10*time.Second {
		m.Interval.Duration = 10 * time.Second
	}
	if m.ErrorLog == nil {
		m.ErrorLog = log.New(ioutil.Discard, "[ERROR] ", log.LstdFlags)
	}
	if m.DebugLog == nil {
		m.DebugLog = log.New(ioutil.Discard, "[DEBUG] ", log.LstdFlags)
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
	go m.processIncomingMessages()
	go m.processOutgoingMessages()
	return nil
}

// Stop cancels the iMessage-sqlite3 db and outgoing message watcher routine(s).
// Outgoing messages stop working when the routines are stopped.
// Incoming messages are ignored after this runs.
func (m *Messages) Stop() {
	defer func() { m.running = false }()
	if m.running {
		m.stopChan <- true
	}
}

// getDB opens a database connection and locks access, so only one reader can
// access the db at once.
func (m *Messages) getDB() (*sqlite.Conn, error) {
	m.Lock()
	m.DebugLog.Print("opening database")
	db, err := sqlite.OpenConn(m.SQLPath, sqlite.SQLITE_OPEN_READONLY)
	m.checkErr(err, "opening database")
	return db, err
}

// closeDB stops reading the sqlite db and unlocks the read lock.
func (m *Messages) closeDB(db *sqlite.Conn) {
	m.DebugLog.Print("closing database")
	if db == nil {
		m.DebugLog.Print("db was nil? not closed")
		return
	}
	defer m.Unlock()
	m.checkErr(db.Close(), "closing database")
}

// checkErr writes an error to Logger if it exists.
func (m *Messages) checkErr(err error, msg string) {
	if err != nil {
		m.ErrorLog.Printf("%s: %q\n", msg, err)
	}
}
