package events_test

import (
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/events"
)

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	b := events.NewBus(4)
	s := b.Watch(nil)
	defer s.Close()
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 1000; i++ {
					b.Publish(events.Event{Type: "x"})
				}
			}()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a subscriber that never reads")
	}
	if got := len(s.C); got != 4 {
		t.Fatalf("buffered %d, want 4", got)
	}
	if got := s.Dropped(); got != 4000-4 {
		t.Fatalf("dropped %d, want %d", got, 4000-4)
	}
	if s.Dropped() != 0 {
		t.Fatal("drop count not reset")
	}
}

func TestDropsOldest(t *testing.T) {
	b := events.NewBus(2)
	ch, cancel := b.Subscribe(nil)
	defer cancel()
	for _, ty := range []string{"a", "b", "c"} {
		b.Publish(events.Event{Type: ty})
	}
	if e := <-ch; e.Type != "b" {
		t.Fatalf("first kept %q, want b", e.Type)
	}
	if e := <-ch; e.Type != "c" {
		t.Fatalf("second kept %q, want c", e.Type)
	}
}

func TestFilter(t *testing.T) {
	b := events.NewBus(8)
	ch, cancel := b.Subscribe(func(e events.Event) bool { return e.DeviceID == "d1" })
	b.Publish(events.Event{Type: "other", DeviceID: "d2"})
	b.Publish(events.Event{Type: "mine", DeviceID: "d1"})
	if e := <-ch; e.Type != "mine" {
		t.Fatalf("got %q", e.Type)
	}
	if len(ch) != 0 {
		t.Fatal("filtered event delivered")
	}
	cancel()
	b.Publish(events.Event{Type: "late", DeviceID: "d1"})
	if len(ch) != 0 {
		t.Fatal("delivered after cancel")
	}
}
