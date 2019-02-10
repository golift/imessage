package imessage

import (
	"errors"
	"sync"
	"time"

	"crawshaw.io/sqlite"
)

// Config is our input data, data store, and interface to methods.
type Config struct {
	ClearMsgs bool          `xml:"clear_messages" json:"clear_messages" toml:"clear_messages" yaml:"clear_messages"`
	QueueSize int           `xml:"queue_size" json:"queue_size" toml:"queue_size" yaml:"queue_size"`
	Retries   int           `xml:"retries" json:"retries" toml:"retries" yaml:"retries"`
	SQLPath   string        `xml:"sql_path" json:"sql_path" toml:"sql_path" yaml:"sql_path"`
	Interval  time.Duration `xml:"interval" json:"interval" toml:"interval" yaml:"interval"`
}

// Messages is the interface into this module.
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
type Logger func(msg string, fmt ...interface{})

// Init reads incoming messages destined for iMessage buddies.
// The messages are queued in a channel and sent 1 at a time with a small
// delay between. Each message may have a callback attached that is kicked
// off in a go routine after the message is sent.
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
	if c.Interval == 0 || c.SQLPath == "" {
		return m, nil
	} else if c.Interval > 10*time.Second {
		c.Interval = 10 * time.Second
	}
	// Try to open, query and close the datbase.
	return m, m.getCurrentID()
}

// Start re-starts the iMessage-sqlite3 db and outgoing message watcher routine(s).
// Outgoing messages wont work until Start() runs.
func (c *Messages) Start() error {
	if c.running {
		return errors.New("already running")
	} else if err := c.getCurrentID(); err != nil {
		return err
	}
	c.running = true
	c.dLogf("starting with id %d", c.startID)
	go c.processIncomingMessages()
	go c.processOutgoingMessages()
	return nil
}

// Stop cancels the iMessage-sqlite3 db and outgoing message watcher routine(s).
// Outgoing messages wont work if the routines are stopped.
func (c *Messages) Stop() {
	defer func() { c.running = false }()
	if c.running {
		c.stopOutgoing <- true
		c.stopIncoming <- true
	}
}

// SetDebugLogger allows a library consumer to do whatever they want with the debug logs from this package.
func (c *Messages) SetDebugLogger(logger Logger) {
	c.DebugLog = logger
}

// SetErrorLogger allows a library consumer to do whatever they want with the error logs from this package.
func (c *Messages) SetErrorLogger(logger Logger) {
	c.ErrorLog = logger
}

func (c *Messages) dLogf(msg string, v ...interface{}) {
	if c.DebugLog != nil {
		c.DebugLog("[DEBUG] "+msg, v...)
	}
}

func (c *Messages) eLogf(msg string, v ...interface{}) {
	if c.ErrorLog != nil {
		c.ErrorLog("[ERROR] "+msg, v...)
	}
}

func (c *Messages) getDB() error {
	c.dbLock.Lock()
	c.dLogf("opening database")
	var err error
	c.db, err = sqlite.OpenConn(c.config.SQLPath, 1)
	return c.checkErr(err, "opening database")
}

func (c *Messages) closeDB() {
	defer c.dbLock.Unlock()
	c.dLogf("closing database")
	if c.db != nil {
		_ = c.checkErr(c.db.Close(), "closing database")
		c.db = nil
	} else {
		c.dLogf("db was nil?")
	}
}

func (c *Messages) checkErr(err error, msg string) error {
	if err != nil {
		c.eLogf("%s: %q\n", msg, err)
	}
	return err
}
