// Package events is the in-process event bus and the admin live feed over it.
package events

import (
	"sync"
	"sync/atomic"
	"time"
)

const defaultBuffer = 256

// Event is one thing that happened in a turn. Data may carry text, so an
// event goes to subscribers only and is never logged.
type Event struct {
	Type                        string
	DeviceID, SessionID, TurnID string
	At                          time.Time
	Data                        any
}

// Publisher is what publishes onto the bus; *Bus is one.
type Publisher interface{ Publish(e Event) }

// Bus fans events out to subscribers. Publish never blocks: a subscriber
// whose buffer is full loses its oldest event.
type Bus struct {
	buffer int
	mu     sync.RWMutex
	subs   map[*Subscription]struct{}
}

func NewBus(buffer int) *Bus {
	if buffer <= 0 {
		buffer = defaultBuffer
	}
	return &Bus{buffer: buffer, subs: map[*Subscription]struct{}{}}
}

// Subscription is one subscriber's bounded buffer.
type Subscription struct {
	C       <-chan Event
	ch      chan Event
	filter  func(Event) bool
	bus     *Bus
	mu      sync.Mutex // serializes publishers, so room made stays room
	dropped atomic.Uint64
	notify  chan struct{} // cap 1: pokes the consumer after a drop
}

// Dropped returns the events lost since the last call.
func (s *Subscription) Dropped() uint64 { return s.dropped.Swap(0) }

// Drops is poked after events were lost.
func (s *Subscription) Drops() <-chan struct{} { return s.notify }

// Close stops delivery; C is not closed.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	delete(s.bus.subs, s)
	s.bus.mu.Unlock()
}

// Watch subscribes to the events filter accepts (nil: all).
func (b *Bus) Watch(filter func(Event) bool) *Subscription {
	ch := make(chan Event, b.buffer)
	s := &Subscription{C: ch, ch: ch, filter: filter, bus: b, notify: make(chan struct{}, 1)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Subscribe is Watch without the drop count; the func unsubscribes.
func (b *Bus) Subscribe(filter func(Event) bool) (<-chan Event, func()) {
	s := b.Watch(filter)
	return s.C, s.Close
}

func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if s.filter == nil || s.filter(e) {
			s.push(e)
		}
	}
}

func (s *Subscription) push(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.ch <- e:
		return
	default:
	}
	// full: only publishers fill the channel and this one holds the lock,
	// so the room made here is still there for the send
	select {
	case <-s.ch:
	default:
	}
	s.dropped.Add(1)
	select {
	case s.notify <- struct{}{}:
	default:
	}
	select {
	case s.ch <- e:
	default:
	}
}
