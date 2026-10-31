// Package bus provides the in-process event fan-out used for SSE streaming and
// for mirroring logs into the persistent event log.
package bus

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/store"
)

// Event is a server-sent event payload.
type Event struct {
	Type string         `json:"type"`
	At   time.Time      `json:"at"`
	Data map[string]any `json:"data,omitempty"`
}

// Bus fans events out to subscribers.
type Bus struct {
	mu   sync.RWMutex
	subs map[int]chan Event
	next int
}

// New creates a bus.
func New() *Bus {
	return &Bus{subs: map[int]chan Event{}}
}

// Subscribe registers a subscriber and returns its channel plus a cancel func.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

// Publish sends an event to every subscriber, dropping for slow consumers.
func (b *Bus) Publish(ev Event) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Slow consumer: drop rather than block the producer.
		}
	}
}

// PublishType is a convenience wrapper.
func (b *Bus) PublishType(typ string, data map[string]any) {
	b.Publish(Event{Type: typ, Data: data})
}

// Marshal renders the event for the SSE wire format.
func (e Event) Marshal() string {
	b, _ := json.Marshal(e.Data)
	return "event: " + e.Type + "\ndata: " + string(b) + "\n\n"
}

// Bridge mirrors logging records into the event log and the bus. It returns a
// stop function.
// Retention bounds how much history is kept; zero disables that dimension.
type Retention struct {
	EventsMax  int
	EventsDays int
	ScansMax   int
	ScansDays  int
}

func Bridge(b *Bus, st *store.Store, retFn func() Retention) func() {
	seen := make(chan struct{})
	var mu sync.Mutex
	last := map[string]time.Time{}
	logging.AddHook(func(r logging.Record) {
		if r.Level != "info" && r.Level != "warn" && r.Level != "error" {
			return
		}
		// Collapse identical messages emitted within a second.
		key := r.Category + "|" + r.Message
		mu.Lock()
		if t, ok := last[key]; ok && time.Since(t) < time.Second {
			mu.Unlock()
			return
		}
		last[key] = time.Now()
		mu.Unlock()

		meta := model.JSONMap{}
		for k, v := range r.Meta {
			meta[k] = v
		}
		if err := st.AddEvent(r.Level, r.Category, r.Message, meta); err != nil {
			return
		}
		b.Publish(Event{Type: "log", Data: map[string]any{
			"level": r.Level, "category": r.Category, "message": r.Message, "meta": meta,
		}})
	})

	go pruneLoop(seen, st, retFn)
	return func() { close(seen) }
}

func pruneLoop(done <-chan struct{}, st *store.Store, retFn func() Retention) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			ret := retFn()
			if ret.EventsMax > 0 {
				_, _ = st.TrimEvents(ret.EventsMax)
			}
			_, _ = st.PurgeExpiredSessions()
			if ret.EventsDays > 0 {
				_, _ = st.PurgeEventsOlderThan(ret.EventsDays)
			}
			if ret.ScansDays > 0 {
				_, _ = st.PurgeScans(ret.ScansDays)
			}
			if ret.ScansMax > 0 {
				_, _ = st.TrimScans(ret.ScansMax)
			}
		}
	}
}
