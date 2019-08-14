# go-imessage

Go Library used to interact with iMessage (Messages.app) on macOS

-  [GODOC](https://godoc.org/golift.io/imessage)

Use this library to send and receive messages using iMessage. I personally use it for
various home automation applications. You can use it to make a chat bot or something
similar. You can bind either a function or a channel to any or all messages.
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

	"golift.io/imessage"
)

func checkErr(err error) {
	if err != nil {
		log.Fatalln(err)
	}
}

func main() {
	iChatDBLocation := "/Users/<your username>/Library/Messages/chat.db"
	c := &imessage.Config{
		SQLPath:   iChatDBLocation, // Set this correctly
		QueueSize: 10,              // 10-20 is fine. If your server is super busy, tune this.
		Retries:   3,               // run the applescript up to this many times to send a message. 3 works well.
	}
	s, err := imessage.Init(c)
	checkErr(err)

	done := make(chan imessage.Incoming) // Make a channel to receive incoming messages.
	s.SetDebugLogger(log.Printf)         // Log debug messages.
	s.SetErrorLogger(log.Printf)         // Log errors.
	s.IncomingChan(".*", done)           // Bind to all incoming messages.
	err = s.Start()                      // Start outgoing and incoming message go routines.
	checkErr(err)
	log.Print("waiting for msgs")

	for msg := range done { // wait here for messages to come in.
		if len(msg.Text) < 60 {
			log.Println("id:", msg.RowID, "from:", msg.From, "attachment?", msg.File, "msg:", msg.Text)
		} else {
			log.Println("id:", msg.RowID, "from:", msg.From, "length:", len(msg.Text))
		}
		if strings.HasPrefix(msg.Text, "Help") {
			// Reply to any incoming message that has the word "Help" as the first word.
			s.Send(imessage.Outgoing{Text: "no help for you", To: msg.From})
		}
	}
}
```
