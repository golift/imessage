package imessage

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	sleepTime = 100 * time.Millisecond
	clearTime = 2 * time.Minute
)

// OSAScriptPath is the path to the osascript binary. macOS only.
//
//nolint:gochecknoglobals
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

// Send is the method used to send an iMessage. Thread/routine safe.
// The messages are queued in a channel and sent 1 at a time with a small
// delay between. Each message may have a callback attached that is kicked
// off in a go routine after the message is sent.
func (m *Messages) Send(msg Outgoing) {
	m.outChan <- msg
}

// RunAppleScript runs a script on the local system. While not directly related to
// iMessage and Messages.app, this library uses AppleScript to send messages using
// imessage. To that end, the method to run scripts is also exposed for convenience.
func (m *Messages) RunAppleScript(scripts []string) (bool, []error) {
	arg := []string{OSAScriptPath}
	for _, s := range scripts {
		arg = append(arg, "-e", s)
	}

	m.DebugLog.Printf("AppleScript Command: %v", strings.Join(arg, " "))

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.Config.Timeout)*time.Second)
	defer cancel()

	var (
		success bool
		errs    []error
	)

	for i := 1; i <= m.Config.Retries && !success; i++ {
		if i > 1 {
			// we had an error, don't be so quick to try again.
			time.Sleep(time.Second)
		}

		cmd := exec.CommandContext(ctx, arg[0], arg[1:]...) //nolint:gosec

		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out

		if err := cmd.Run(); err != nil {
			errs = append(errs, fmt.Errorf("exec: %w: %v", err, out.String()))
			continue
		}

		success = true
	}

	return success, errs
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
	if sent, err := m.RunAppleScript([]string{arg}); !sent && err != nil {
		return err[0]
	}

	time.Sleep(sleepTime)

	return nil
}

// processOutgoingMessages keeps an eye out for outgoing messages; then processes them.
func (m *Messages) processOutgoingMessages() {
	clearTicker := time.NewTicker(clearTime)
	defer clearTicker.Stop()

	newMsg := true

	for {
		select {
		case msg, ok := <-m.outChan:
			if !ok {
				return
			}

			newMsg = true
			response := m.sendiMessage(msg)

			if msg.Call != nil {
				go msg.Call(response)
			}
		case <-clearTicker.C:
			if m.ClearMsgs && newMsg {
				newMsg = false

				m.DebugLog.Print("Clearing Messages.app Conversations")
				m.checkErr(m.ClearMessages(), "clearing messages")
			}
		}
	}
}

// sendiMessage runs the applesripts to send a message and close the iMessage windows.
func (m *Messages) sendiMessage(msg Outgoing) *Response {
	arg := []string{`tell application "Messages" to send "` + msg.Text + `" to buddy "` + msg.To +
		`" of (1st service whose service type = iMessage)`}

	if _, err := os.Stat(msg.Text); err == nil && msg.File {
		arg = []string{`tell application "Messages" to send (POSIX file ("` + msg.Text + `")) to buddy "` + msg.To +
			`" of (1st service whose service type = iMessage)`}
	}

	arg = append(arg, `tell application "Messages" to close every window`)
	sent, errs := m.RunAppleScript(arg)
	// Messages can go out so quickly we need to sleep a bit to avoid sending duplicates.
	time.Sleep(sleepTime)

	return &Response{ID: msg.ID, To: msg.To, Text: msg.Text, Errs: errs, Sent: sent}
}
