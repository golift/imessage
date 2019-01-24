# go-imessage

Go Library used to interact with iMessage (Messages.app) on macOS

I'll make some docs eventually. For now:

Use this library to send and receive messages using iMessage. I personally use it for
various home automation applications. You can use it to make a chat bot or something
similar. You can find bind either a function or a channel to any or all messages.
The Send() method uses AppleScript, which is likely going to require some tinkering.
You got this far, so I trust you'll figure that out. Let me know how it works out.

The library uses `fsnotify` to poll for db updates, then checks the database for changes.
Only new messages are processed. If somehow `fsnotify` fails it will fall back to polling
the database. Pay attention to the debug/error logs. See the example below for an easy
way to log the library messages.


A working example:
```golang
package main

import (
	"log"
	"strings"
	"time"

	"github.com/golift/imessage"
)

func checkErr(err error) {
	if err != nil {
		log.Fatalln(err)
	}
}

func main() {
	c := &imessage.Config{
		SQLPath:   "/Users/<your username>/Library/Messages/chat.db", // Set this correctly
		QueueSize: 10, // 10-20 is fine. If your server is super busy, tune this.
		Retries:   3, // run the applescript up to this many times to send a message. 3 works well.
		Interval:  250 * time.Millisecond, // minimum duration between db polls.
	}
	s, err := imessage.Init(c)
	checkErr(err)
	done := make(chan imessage.Incoming)
	s.SetDebugLogger(log.Printf)
	s.SetErrorLogger(log.Printf)
	s.IncomingChan("*", done)
	checkErr(s.Start())
	log.Print("waiting for msgs")
	for msg := range done {
		if len(msg.Text) < 60 {
			log.Println("id:", msg.RowID, "from:", msg.From, "attachment?", msg.File, "msg:", msg.Text)
		} else {
			log.Println("id:", msg.RowID, "from:", msg.From, "length:", len(msg.Text))
		}
		if strings.Contains(msg.Text, "Help") {
			// Reply to any incoming message with the word "Help"
			s.Send(imessage.Outgoing{Text: "no help for you", To: msg.From})
		}
	}
}
```
