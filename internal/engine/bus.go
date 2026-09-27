// Package engine holds the long-lived parts of the server: the state
// machine, the scheduler that wakes on next_due_at, the delivery
// dispatcher and the in-process bus that ties them to the service layer.
package engine

import (
	"sync"
	"sync/atomic"
)

// MonitorChanged is published after every committed change to a monitor
// row: create, update, delete, pause, resume and every state flip.
type MonitorChanged struct {
	ProjectID string
	MonitorID string
	Deleted   bool
}

// Bus is a small typed publish/subscribe channel. Publishing never blocks:
// a subscriber that is not keeping up loses events, and Dropped counts
// them. Subscribers treat an event as "something changed, reload".
type Bus struct {
	mu      sync.RWMutex
	subs    map[int]chan MonitorChanged
	nextID  int
	Dropped atomic.Int64
}

// NewBus returns an empty bus.
func NewBus() *Bus { return &Bus{subs: map[int]chan MonitorChanged{}} }

// Publish delivers e to every subscriber without blocking.
func (b *Bus) Publish(e MonitorChanged) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
			b.Dropped.Add(1)
		}
	}
}

// Subscribe returns a buffered channel of events and a cancel function.
func (b *Bus) Subscribe() (<-chan MonitorChanged, func()) {
	ch := make(chan MonitorChanged, 64)
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}
