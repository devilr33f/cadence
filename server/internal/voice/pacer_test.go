package voice

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func allLive(packet) bool { return true }

func TestBurstThenTick(t *testing.T) {
	packets := make(chan packet, 25)
	for i := range 25 {
		packets <- packet{opus: []byte{byte(i)}}
	}
	close(packets)
	tick := make(chan time.Time)
	got := make(chan byte, 25)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pace(context.Background(), packets, allLive, func(p []byte) error { got <- p[0]; return nil }, tick)
	}()

	next := func(want byte) {
		t.Helper()
		select {
		case b := <-got:
			if b != want {
				t.Fatalf("packet %d, want %d", b, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("packet %d not sent", want)
		}
	}
	quiet := func() {
		t.Helper()
		select {
		case b := <-got:
			t.Fatalf("packet %d sent without a tick", b)
		case <-time.After(30 * time.Millisecond):
		}
	}
	for i := range 10 {
		next(byte(i))
	}
	quiet()
	for i := 10; i < 25; i++ {
		tick <- time.Now()
		next(byte(i))
		quiet()
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pace did not return after the last packet")
	}
}

// regression: the reply's ticker runs from before the first packet, so a tick
// was due when the burst ended and let an 11th packet through at once
func TestStaleTickAfterBurstIgnored(t *testing.T) {
	packets := make(chan packet, 12)
	for i := range 12 {
		packets <- packet{opus: []byte{byte(i)}}
	}
	close(packets)
	tick := make(chan time.Time, 1)
	tick <- time.Now()
	got := make(chan byte, 12)
	go pace(context.Background(), packets, allLive, func(p []byte) error { got <- p[0]; return nil }, tick)

	for i := range 10 {
		select {
		case <-got:
		case <-time.After(time.Second):
			t.Fatalf("burst packet %d not sent", i)
		}
	}
	select {
	case b := <-got:
		t.Fatalf("packet %d sent on a tick that fell due during the burst", b)
	case <-time.After(30 * time.Millisecond):
	}
	tick <- time.Now()
	select {
	case b := <-got:
		if b != 10 {
			t.Fatalf("packet %d, want 10", b)
		}
	case <-time.After(time.Second):
		t.Fatal("packet 10 not sent on a fresh tick")
	}
}

// discarded packets go without a tick, so later speech is not held back by
// audio nobody hears; the one waiting for its tick is dropped too
func TestDiscardedPacketsSpendNoTick(t *testing.T) {
	packets := make(chan packet, 18)
	for i := range 15 {
		packets <- packet{gen: 0, opus: []byte{byte(i)}}
	}
	for i := 15; i < 18; i++ {
		packets <- packet{gen: 1, opus: []byte{byte(i)}}
	}
	close(packets)
	var gen atomic.Uint64
	var once sync.Once
	checked := make(chan struct{})
	live := func(p packet) bool {
		if p.opus[0] == 10 {
			once.Do(func() { close(checked) })
		}
		return p.gen == gen.Load()
	}
	tick := make(chan time.Time)
	got := make(chan byte, 18)
	go pace(context.Background(), packets, live, func(p []byte) error { got <- p[0]; return nil }, tick)
	tickOnce := func() {
		t.Helper()
		select {
		case tick <- time.Now():
		case <-time.After(time.Second):
			t.Fatal("pace took no tick")
		}
	}

	for i := range 10 {
		select {
		case <-got:
		case <-time.After(time.Second):
			t.Fatalf("burst packet %d not sent", i)
		}
	}
	// packet 10 was live when it came, so it waits for a tick
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("packet 10 never checked")
	}
	gen.Store(1)
	// that tick drops it, and 11 to 14 go at once
	tickOnce()
	for want := byte(15); want < 18; want++ {
		tickOnce()
		select {
		case b := <-got:
			if b != want {
				t.Fatalf("packet %d, want %d", b, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("packet %d not sent", want)
		}
	}
}
