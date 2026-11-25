package cec

import (
	"context"
	"sync"
)

// Adapter is the bus's own side of the wire as its holder knows it: a screen board is a playback device at
// its own physical address; Present false when there is no bus at all (no /dev/cec, a platform that keeps
// an app from cec).
type Adapter struct {
	Present         bool
	PhysicalAddress string
	LogicalAddress  int // -1 while unallocated or unknown
	DeviceType      string
}

// Bus is the cec wire a board reaches it through. Every method must be safe to call from any goroutine;
// sinks are called from one goroutine of the bus's own, never after Start's context ends.
type Bus interface {
	// Info is the adapter as its side knows it; a bus that has not started may still answer, from what it
	// read the last time it ran.
	Info() Adapter
	// Send transmits msg (its initiator nibble is the bus's to fill in) and returns ok when the target
	// acknowledged it. With want set it waits for that opcode's reply and returns the frame that carried it
	// (a Feature Abort of want is not it); ok false when the reply did not come.
	Send(ctx context.Context, msg Frame, want byte) (Frame, bool)
	// Start runs the wire until ctx ends; every frame it carries that Send did not take as its reply
	// reaches each sink registered before Start.
	Start(ctx context.Context, wg *sync.WaitGroup)
	// Subscribe adds a sink for the frames Start reports; call before Start.
	Subscribe(sink func(Frame))
}

// sinks fans one frame out to every subscriber, one at a time
type sinks struct {
	mu   sync.Mutex
	list []func(Frame)
}

func (s *sinks) add(f func(Frame)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, f)
}

func (s *sinks) deliver(f Frame) {
	s.mu.Lock()
	list := s.list
	s.mu.Unlock()
	for _, sink := range list {
		sink(f)
	}
}
