package imessage

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// Outgoing struct is used to send a message to someone.
// Fll it out and pass it into config.Send()
type Outgoing struct {
	ID   string          // ID is only used in logging and in the Response callback.
	To   string          // To represents the message recipient.
	Text string          // Text is the body of the message or file path.
	File bool            // If File is true, then Text is assume to be a filepath to send.
	Call func(*Response) // Call is the function that is run after a message is sent off.
}

// Response is the outgoing-message response provided to a callback function.
type Response struct {
	ID   string
	To   string
	Text string
	Errs []error
}

// Send a message.
func (c *Config) Send(msg Outgoing) {
	c.outChan <- msg
}

// RunAppleScript runs a script on the local system.
func (c *Config) RunAppleScript(id string, scripts []string, retry int) (errs []error) {
	arg := []string{"/usr/bin/osascript"}
	for _, s := range scripts {
		arg = append(arg, "-e", s)
	}
	c.dLogf("[%v] AppleScript Command: %v", id, strings.Join(arg, " "))
	for i := 1; i <= retry; i++ {
		var out bytes.Buffer
		cmd := exec.Command(arg[0], arg[1:]...)
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err == nil {
			break
		} else if i >= retry {
			errs = append(errs, errors.Wrapf(errors.New(out.String()), "cmd.Run: %v", err.Error()))
			return
		} else {
			errs = append(errs, err)
			c.eLogf("[%v] (%v/%v) cmd.Run: %v: %v", id, i, retry, err, out.String())
		}
		time.Sleep(750 * time.Millisecond)
	}
	return nil
}

// ClearMessages deletes all conversations in MESSAGES.APP.
// Use this only if Messages is behaving poorly. Or, never use it at all.
// This probably doesn't do anything you want to do.
func (c *Config) ClearMessages() error {
	arg := `tell application "Messages"
	activate
	try
		repeat (count of (get every chat)) times
			tell application "System Events" to tell process "Messages" to keystroke return
			delete item 1 of (get every chat)
			tell application "System Events" to tell process "Messages" to keystroke return
		end repeat
	end try
	close every window
end tell
`
	if err := c.RunAppleScript("wipe", []string{arg}, 1); err != nil {
		return err[0]
	}
	time.Sleep(75 * time.Millisecond)
	return nil
}

// processOutgoingMessages keeps an eye out for outgoing messages; then processes them.
func (c *Config) processOutgoingMessages() {
	newMsg := true
	clearTicker := time.NewTicker(2 * time.Minute).C
	for {
		select {
		case msg := <-c.outChan:
			newMsg = true
			err := c.sendiMessage(msg)
			if msg.Call != nil {
				go msg.Call(&Response{ID: msg.ID, To: msg.To, Text: msg.Text, Errs: err})
			}
			// Give iMessage time to do its thing.
			time.Sleep(300 * time.Millisecond)
		case <-clearTicker:
			if c.ClearMsgs && newMsg {
				newMsg = false
				c.dLogf("Clearing Messages.app Conversations")
				_ = c.checkErr(c.ClearMessages(), "clearing messages")
				time.Sleep(time.Second)
			}
		case <-c.stopOutgoing:
			return
		}
	}
}

func (c *Config) sendiMessage(m Outgoing) []error {
	arg := []string{`tell application "Messages" to send "` + m.Text + `" to buddy "` + m.To +
		`" of (1st service whose service type = iMessage)`}
	if _, err := os.Stat(m.Text); err == nil && m.File {
		arg = []string{`tell application "Messages" to send (POSIX file ("` + m.Text + `")) to buddy "` + m.To +
			`" of (1st service whose service type = iMessage)`}
	}
	arg = append(arg, `tell application "Messages" to close every window`)
	if errs := c.RunAppleScript(m.ID, arg, 3); errs != nil {
		return errs
	}
	if !m.File {
		// Text messages go out so quickly we need to sleep a bit to avoid sending duplicates.
		time.Sleep(300 * time.Millisecond)
	}
	return nil
}
