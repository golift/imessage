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
	config       *Config
	ErrorLog     Logger
	DebugLog     Logger
	running      bool
	startID      int64
	outChan      chan Outgoing
	inChan       chan Incoming
	stopOutgoing chan bool
	stopIncoming chan bool
	db           *sqlite.Conn
	dbLock       sync.Mutex
	funcBinds    []*funcBinding
	chanBinds    []*chanBinding
	chanLock     sync.RWMutex
	funcLock     sync.RWMutex
}

// Logger is a base type to deal with changing log outs.
// Pass a matching interface (like log.Printf) to capture
// messages from the running background go routines.
type Logger func(msg string, fmt ...interface{})

// Init is the primary function to retrieve a Message handler.
// Pass a Config struct in and use the returned Messages struct to send
// and respond to incoming messages.
func Init(c *Config) (*Messages, error) {
	m := &Messages{
		config:       c,
		outChan:      make(chan Outgoing, c.QueueSize),
		inChan:       make(chan Incoming, c.QueueSize),
		stopIncoming: make(chan bool),
		stopOutgoing: make(chan bool),
	}
	if c.Retries == 0 {
		c.Retries = 1
	} else if c.Retries > 10 {
		c.Retries = 10
	}
	if c.SQLPath == "" {
		return m, nil
	}
	if c.Interval.Duration == 0 {
		c.Interval.Duration = DefaultDuration
	} else if c.Interval.Duration > 10*time.Second {
		c.Interval.Duration = 10 * time.Second
	}
	// Try to open, query and close the database.
	return m, m.getCurrentID()
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
	m.dLogf("starting with id %d", m.startID)
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
		m.stopOutgoing <- true
		m.stopIncoming <- true
	}
}

// SetDebugLogger allows a library consumer to do whatever they want with the debug logs from this package.
// Pass in a Logger interface like log.Printf to capture messages created by the go routines.
// The DebugLog struct item is exported and can also be set directly without a method call.
func (m *Messages) SetDebugLogger(logger Logger) {
	m.DebugLog = logger
}

// SetErrorLogger allows a library consumer to do whatever they want with the error logs from this package.
// Pass in a Logger interface like log.Printf to capture messages created by the go routines.
// The ErrorLog struct item is exported and can also be set directly without a method call.
func (m *Messages) SetErrorLogger(logger Logger) {
	m.ErrorLog = logger
}

// dLogf logs a debug message.
func (m *Messages) dLogf(msg string, v ...interface{}) {
	if m.DebugLog != nil {
		m.DebugLog("[DEBUG] "+msg, v...)
	}
}

// eLogf logs an error message.
func (m *Messages) eLogf(msg string, v ...interface{}) {
	if m.ErrorLog != nil {
		m.ErrorLog("[ERROR] "+msg, v...)
	}
}

// getDB opens a database connection and locks access, so only one reader can
// access the db at once.
func (m *Messages) getDB() error {
	m.dbLock.Lock()
	m.dLogf("opening database")
	var err error
	m.db, err = sqlite.OpenConn(m.config.SQLPath, 1)
	return m.checkErr(err, "opening database")
}

// closeDB stops reading the sqlite db and unlocks the read lock.
func (m *Messages) closeDB() {
	defer m.dbLock.Unlock()
	m.dLogf("closing database")
	if m.db != nil {
		_ = m.checkErr(m.db.Close(), "closing database")
		m.db = nil
	} else {
		m.dLogf("db was nil?")
	}
}

// checkErr writes an error to Logger if it exists.
func (m *Messages) checkErr(err error, msg string) error {
	if err != nil {
		m.eLogf("%s: %q\n", msg, err)
	}
	return err
}
