package hostruntime

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// eventLog persists every bridge-side event, including events the session state
// machine ignores. The observer worker writes observation/events.jsonl from a
// different process, so this channel is a separate append-only file.
// Only diagnostic metadata is logged: never CDP payloads, cookies or tickets.
type eventLog struct {
	mu     sync.Mutex
	f      *os.File
	began  time.Time
	closed bool
}

func newEventLog(path string) (*eventLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &eventLog{f: f, began: time.Now()}, nil
}

func (l *eventLog) Log(event string, payload map[string]any) {
	row := map[string]any{"event": event, "elapsed": time.Since(l.began).Seconds()}
	if len(payload) > 0 {
		row["payload"] = payload
	}
	data, err := json.Marshal(row)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.f.Write(append(data, '\n'))
	l.f.Sync()
}

func (l *eventLog) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	l.f.Close()
}
