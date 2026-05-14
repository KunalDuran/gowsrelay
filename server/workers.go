package server

import (
	"log"
	"os"
)

// FileWorker returns a WorkerFunc that appends each message to a file.
//
// Recognised opts keys:
//   - "path" (string, required) — file to write to; created if absent.
//   - "from" (Role, optional)   — restrict recording to one role (Producer or
//     Subscriber); omit to record all messages.
func FileWorker() WorkerFunc {
	return func(msg Message, opts map[string]any) {
		path, ok := opts["path"].(string)
		if !ok || path == "" {
			log.Print("file worker: missing or empty 'path' opt")
			return
		}

		if from, ok := opts["from"].(Role); ok && msg.From != from {
			return
		}

		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("file worker: open %s: %v", path, err)
			return
		}
		defer f.Close()

		if _, err := f.Write(msg.Data); err != nil {
			log.Printf("file worker: write %s: %v", path, err)
			return
		}
		f.Write([]byte{'\n'})
	}
}
