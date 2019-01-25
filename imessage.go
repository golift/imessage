package imessage

import (
	"errors"
	"sync"
	"time"

	"crawshaw.io/sqlite"
)

// Config is our input data, data store, and interface to methods.
type Config struct {
	ClearMsgs    bool          `xml:"clear_messages" json:"clear_messages" toml:"clear_messages" yaml:"clear_messages"`
	QueueSize    int           `xml:"queue_size" json:"queue_size" toml:"queue_size" yaml:"queue_size"`
	Retries      int           `xml:"retries" json:"retries" toml:"retries" yaml:"retries"`
	SQLPath      string        `xml:"sql_path" json:"sql_path" toml:"sql_path" yaml:"sql_path"`
	Interval     time.Duration `xml:"interval" json:"interval" toml:"interval" yaml:"interval"`
	ErrorLog     Logger
	DebugLog     Logger
	running      bool
	startID      int64
	funcBinds    []*funcBinding
	chanBinds    []*chanBinding
	outChan      chan Outgoing
	inChan       chan Incoming
	stopOutgoing chan bool
	stopIncoming chan bool
	db           *sqlite.Conn
	dblock       sync.Mutex
}

// Messages is the interface into this module.
type Messages interface {
	RunAppleScript(id string, scripts []string, retry int) []error
	Send(msg Outgoing)
	ClearMessages() error
	SetDebugLogger(logger Logger)
	SetErrorLogger(logger Logger)
	IncomingCall(match string, callback func(Incoming))
	IncomingChan(match string, channel chan Incoming)
	RemoveChan(match string) int
	RemoveCall(match string) int
	Start() error
	Stop()
}

// Logger is a base type to deal with changing log outs.
type Logger func(msg string, fmt ...interface{})

// Init reads incoming messages destined for iMessage buddies.
// The messages are queued in a channel and sent 1 at a time with a small
// delay between. Each message may have a callback attached that is kicked
// off in a go routine after the message is sent.
func Init(c *Config) (Messages, error) {
	if c.Retries == 0 {
		c.Retries = 1
	} else if c.Retries > 10 {
		c.Retries = 10
	}
	c.outChan = make(chan Outgoing, c.QueueSize)
	c.inChan = make(chan Incoming, c.QueueSize)
	c.stopIncoming = make(chan bool)
	c.stopOutgoing = make(chan bool)
	if c.Interval == 0 || c.SQLPath == "" {
		return c, nil
	} else if c.Interval > 10*time.Second {
		c.Interval = 10 * time.Second
	}
	// Try to open, query and close the datbase.
	if err := c.getCurrentID(); err != nil {
		return c, err
	}
	return c, nil
}

// Start re-starts the iMessage-sqlite3 db and outgoing message watcher routine(s).
// Outgoing messages wont work until Start() runs.
func (c *Config) Start() error {
	if !c.running {
		if err := c.getCurrentID(); err != nil {
			return err
		}
		c.running = true
		c.dLogf("starting with id %d", c.startID)
		go c.processIncomingMessages()
		go c.processOutgoingMessages()
		return nil
	}
	return errors.New("already running")
}

// Stop cancels the iMessage-sqlite3 db and outgoing message watcher routine(s).
// Outgoing messages wont work if the routines are stopped.
func (c *Config) Stop() {
	if c.running {
		c.running = false
		c.stopOutgoing <- true
		c.stopIncoming <- true
	}
}

// SetDebugLogger allows a library consumer to do whatever they want with the debug logs from this package.
func (c *Config) SetDebugLogger(logger Logger) {
	c.DebugLog = logger
}

// SetErrorLogger allows a library consumer to do whatever they want with the error logs from this package.
func (c *Config) SetErrorLogger(logger Logger) {
	c.ErrorLog = logger
}

func (c *Config) dLogf(msg string, v ...interface{}) {
	if c.DebugLog != nil {
		c.DebugLog("[DEBUG] "+msg, v...)
	}
}

func (c *Config) eLogf(msg string, v ...interface{}) {
	if c.ErrorLog != nil {
		c.ErrorLog("[ERROR] "+msg, v...)
	}
}

func (c *Config) getDB() error {
	c.dblock.Lock()
	c.dLogf("opening database")
	var err error
	c.db, err = sqlite.OpenConn(c.SQLPath, 1)
	return c.checkErr(err, "opening database")
}

func (c *Config) closeDB() {
	defer c.dblock.Unlock()
	c.dLogf("closing database")
	if c.db != nil {
		_ = c.checkErr(c.db.Close(), "closing database")
		c.db = nil
	} else {
		c.dLogf("db was nil?")
	}
}

func (c *Config) checkErr(err error, msg string) error {
	if err != nil {
		c.eLogf("%s: %q\n", msg, err)
	}
	return err
}
