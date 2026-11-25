package tasks

import (
	"context"
	"fmt"
	"sync"
)

// FakeKind is a scripted Kind for tests: refs are fake-1, fake-2, …; errors
// and snapshots are set by the test.
type FakeKind struct {
	KindName string
	ch       chan Event

	mu                                                   sync.Mutex
	n                                                    int
	StartErr, SendErr, CancelErr, InspectErr, ApproveErr error
	Snapshots                                            map[string]Snapshot
	Specs                                                []Spec
	Sent                                                 []string // "ref:msg"
	Tokens                                               []string // of each Send
	Cancelled                                            []string
	Approved                                             []string // "ref:token:decision"
	Inspected                                            []string
	// OnStart runs inside Start, after the ref is made and before it returns:
	// the way to emit an event for a task the manager has no ref for yet.
	OnStart func(ref string)
	// OnCancel runs inside Cancel, before it returns.
	OnCancel func(ref string)
	// OnApprove runs inside Approve, before it returns.
	OnApprove func(ref string)
}

func NewFakeKind(name string) *FakeKind {
	return &FakeKind{KindName: name, ch: make(chan Event, 64), Snapshots: map[string]Snapshot{}}
}

func (f *FakeKind) Name() string { return f.KindName }

func (f *FakeKind) Start(_ context.Context, s Spec) (string, error) {
	f.mu.Lock()
	if f.StartErr != nil {
		err := f.StartErr
		f.mu.Unlock()
		return "", err
	}
	f.n++
	ref := fmt.Sprintf("fake-%d", f.n)
	f.Specs = append(f.Specs, s)
	hook := f.OnStart
	f.mu.Unlock()
	if hook != nil {
		hook(ref)
	}
	return ref, nil
}

func (f *FakeKind) Send(_ context.Context, ref, token, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sent = append(f.Sent, ref+":"+msg)
	f.Tokens = append(f.Tokens, token)
	return f.SendErr
}

func (f *FakeKind) Cancel(_ context.Context, ref string) error {
	f.mu.Lock()
	f.Cancelled = append(f.Cancelled, ref)
	err, hook := f.CancelErr, f.OnCancel
	f.mu.Unlock()
	if hook != nil {
		hook(ref)
	}
	return err
}

func (f *FakeKind) Inspect(_ context.Context, ref string, _ Detail) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Inspected = append(f.Inspected, ref)
	if f.InspectErr != nil {
		return Snapshot{}, f.InspectErr
	}
	s, ok := f.Snapshots[ref]
	if !ok {
		return Snapshot{Status: StatusUnknown, ErrorCode: "not_found"}, nil
	}
	return s, nil
}

func (f *FakeKind) Events() <-chan Event { return f.ch }

// Emit queues an event the manager will fan in.
func (f *FakeKind) Emit(e Event) { f.ch <- e }

// Stop closes the event channel, as a kind that shut down.
func (f *FakeKind) Stop() { close(f.ch) }

func (f *FakeKind) Approve(_ context.Context, ref, token, decision string) error {
	f.mu.Lock()
	f.Approved = append(f.Approved, ref+":"+token+":"+decision)
	err, hook := f.ApproveErr, f.OnApprove
	f.mu.Unlock()
	if hook != nil {
		hook(ref)
	}
	return err
}
