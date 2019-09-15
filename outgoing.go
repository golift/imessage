package imessage

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// OSAScriptPath is the path to the osascript binary. macOS only.
var OSAScriptPath = "/usr/bin/osascript"

// Outgoing struct is used to send a message to someone.
// Fll it out and pass it into Messages.Send() to fire off a new iMessage.
type Outgoing struct {
	ID   string          // ID is only used in logging and in the Response callback.
	To   string          // To represents the message recipient.
	Text string          // Text is the body of the message or file path.
	File bool            // If File is true, then Text is assume to be a filepath to send.
	Call func(*Response) // Call is the function that is run after a message is sent off.
}

// Response is the outgoing-message response provided to a callback function.
// An outgoing callback function will receive this type. It represents "what happeened"
// when trying to send a message. If `Sent` is false, `Errs` should contain error(s).
type Response struct {
	ID   string
	To   string
	Text string
	Sent bool
	Errs []error
}

// Send is the method used to send an iMessage.
// The messages are queued in a channel and sent 1 at a time with a small
// delay between. Each message may have a callback attached that is kicked
// off in a go routine after the message is sent.
func (m *Messages) Send(msg Outgoing) {
	m.outChan <- msg
}

// RunAppleScript runs a script on the local system. While not directly related to
// iMessage and Messages.app, this library uses AppleScript to send messages using
// imessage. To that end, the method to run scripts is also exposed for convenience.
func (m *Messages) RunAppleScript(id string, scripts []string) (success bool, errs []error) {
	arg := []string{OSAScriptPath}
	for _, s := range scripts {
		arg = append(arg, "-e", s)
	}
	m.DebugLog.Printf("[%v] AppleScript Command: %v", id, strings.Join(arg, " "))
	for i := 1; i <= m.Config.Retries; i++ {
		var out bytes.Buffer
		cmd := exec.Command(arg[0], arg[1:]...)
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err == nil {
			success = true
			break
		} else if i >= m.Config.Retries {
			errs = append(errs, fmt.Errorf("exec.Command: %v: %v", err, out.String()))
			return
		} else {
			errs = append(errs, err)
			m.ErrorLog.Printf("[%v] (%v/%v) exec.Command: %v: %v", id, i, m.Config.Retries, err, out.String())
		}
		time.Sleep(500 * time.Millisecond)
	}
	return
}

// ClearMessages deletes all conversations in MESSAGES.APP.
// Use this only if Messages is behaving poorly. Or, never use it at all.
// This probably doesn't do anything you want to do.
func (m *Messages) ClearMessages() error {
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
	if success, err := m.RunAppleScript("wipe", []string{arg}); !success && err != nil {
		return err[0]
	}
	time.Sleep(time.Second)
	return nil
}

// processOutgoingMessages keeps an eye out for outgoing messages; then processes them.
func (m *Messages) processOutgoingMessages() {
	clearTicker := time.NewTicker(2 * time.Minute).C
	for newMsg := true; ; {
		select {
		case msg, ok := <-m.outChan:
			if !ok {
				return
			}
			newMsg = true
			success, err := m.sendiMessage(msg)
			if msg.Call != nil {
				go msg.Call(&Response{ID: msg.ID, To: msg.To, Text: msg.Text, Errs: err, Sent: success})
			}
		case <-clearTicker:
			if m.ClearMsgs && newMsg {
				newMsg = false
				m.DebugLog.Print("Clearing Messages.app Conversations")
				m.checkErr(m.ClearMessages(), "clearing messages")
			}
		}
	}
}

// sendiMessage runs the applesripts to send a message and close the iMessage windows.
func (m *Messages) sendiMessage(msg Outgoing) (bool, []error) {
	arg := []string{`tell application "Messages" to send "` + msg.Text + `" to buddy "` + msg.To +
		`" of (1st service whose service type = iMessage)`}
	if _, err := os.Stat(msg.Text); err == nil && msg.File {
		arg = []string{`tell application "Messages" to send (POSIX file ("` + msg.Text + `")) to buddy "` + msg.To +
			`" of (1st service whose service type = iMessage)`}
	}
	arg = append(arg, `tell application "Messages" to close every window`)
	success, errs := m.RunAppleScript(msg.ID, arg)
	// Messages can go out so quickly we need to sleep a bit to avoid sending duplicates.
	time.Sleep(100 * time.Millisecond)
	return success, errs
}
