//go:build !linux

package cec

import (
	"context"
	"log/slog"
	"sync"
)

// Stub is OpenLinux where /dev/cec never is (a dev machine, the app's android): a bus that stays absent.
type Stub struct{}

// OpenLinux answers a bus that is never present off linux.
func OpenLinux(string, *slog.Logger) *Stub { return &Stub{} }

// Info says absent.
func (s *Stub) Info() Adapter { return Adapter{} }

// Subscribe keeps the seam whole; nothing ever arrives.
func (s *Stub) Subscribe(func(Frame)) {}

// Start ends with ctx.
func (s *Stub) Start(ctx context.Context, wg *sync.WaitGroup) {
	wg.Go(func() { <-ctx.Done() })
}

// Send never goes anywhere.
func (s *Stub) Send(context.Context, Frame, byte) (Frame, bool) { return Frame{}, false }

var _ Bus = (*Stub)(nil)
