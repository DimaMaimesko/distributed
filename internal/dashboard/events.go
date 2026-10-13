package dashboard

import (
	"encoding/json"
	"sync"
	"time"
)

// maxEventBytes caps the payload kept per event; larger ones are stored
// without data.
const maxEventBytes = 4 << 10

type Event struct {
	ReceivedAt time.Time       `json:"received_at"`
	Subject    string          `json:"subject"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// EventLog keeps the most recent events in a fixed-size ring.
type EventLog struct {
	mu    sync.Mutex
	ring  []Event
	next  int
	total int64
}

func NewEventLog(size int) *EventLog {
	return &EventLog{ring: make([]Event, 0, size)}
}

// Add records an event. Data that is not valid JSON or too large is
// dropped so the page never receives broken JSON.
func (l *EventLog) Add(subject string, data []byte) {
	e := Event{ReceivedAt: time.Now().UTC(), Subject: subject}
	if len(data) <= maxEventBytes && json.Valid(data) {
		e.Data = append(json.RawMessage(nil), data...)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.total++
	if len(l.ring) < cap(l.ring) {
		l.ring = append(l.ring, e)
		return
	}
	l.ring[l.next] = e
	l.next = (l.next + 1) % len(l.ring)
}

// Recent returns the kept events, newest first, and how many were ever added.
func (l *EventLog) Recent() ([]Event, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, 0, len(l.ring))
	for i := range len(l.ring) {
		// Walk backwards from the newest entry.
		idx := (l.next - 1 - i + 2*len(l.ring)) % len(l.ring)
		out = append(out, l.ring[idx])
	}
	return out, l.total
}
