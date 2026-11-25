package volume_test

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

// another writer may have moved the hardware since the level held: the re-check puts the level in force back with a
// plain set, never over a ring's ramp nor from a default the device does not own, and logs only a difference it knows
func TestRecheckPutsTheLevelInForceBack(t *testing.T) {
	var ringing atomic.Bool
	unknown := volume.NewSetter(volume.NewStore(t.TempDir()), 50, nil)
	ru := follow(unknown)
	if unknown.Recheck(ringing.Load) || len(ru.applied) != 0 {
		t.Fatalf("an unknown level put back: %v", ru.applied)
	}

	dir := t.TempDir()
	stored(t, dir, 30)
	logs := &bytes.Buffer{}
	s := volume.NewSetter(volume.NewStore(dir), 50, slog.New(slog.NewTextHandler(logs, nil)))
	r := follow(s)
	tr := &ramp{}
	s.FollowTransient(tr.apply)
	if !s.Recheck(nil) || !s.Recheck(ringing.Load) || !slices.Equal(r.applied, []int{30, 30}) || len(r.heard) != 0 {
		t.Fatalf("recheck applied %v, heard %v", r.applied, r.heard)
	}
	if strings.Contains(logs.String(), "volume_reapplied") {
		t.Fatalf("no difference logged: %s", logs)
	}

	if _, err := s.Transient(45); err != nil {
		t.Fatal(err)
	}
	ringing.Store(true)
	if s.Recheck(ringing.Load) || len(r.applied) != 2 {
		t.Fatalf("a ring's ramp put back: %v", r.applied)
	}
	ringing.Store(false)
	if !s.Recheck(ringing.Load) || !slices.Equal(r.applied, []int{30, 30, 30}) {
		t.Fatalf("after the ring applied %v", r.applied)
	}
	if !strings.Contains(logs.String(), "msg=volume_reapplied found=45 level=30") || strings.Count(logs.String(), "volume_reapplied") != 1 {
		t.Fatalf("log: %s", logs)
	}
}

// while media plays the level in force goes back every interval; while it does not, nothing happens
func TestRecheckWhilePlaying(t *testing.T) {
	dir := t.TempDir()
	stored(t, dir, 30)
	s := volume.NewSetter(volume.NewStore(dir), 50, nil)
	var mu sync.Mutex
	var applied []int
	s.Follow(func(int) func(int) {
		return func(l int) {
			mu.Lock()
			defer mu.Unlock()
			applied = append(applied, l)
		}
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(applied)
	}
	var playing, ringing atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.RecheckWhilePlaying(ctx, 5*time.Millisecond, playing.Load, ringing.Load) }()
	t.Cleanup(func() { cancel(); <-done })

	time.Sleep(50 * time.Millisecond)
	if n := count(); n != 0 {
		t.Fatalf("%d applied with nothing playing", n)
	}
	playing.Store(true)
	waitFor(t, "two rechecks while playing", func() bool { return count() >= 2 })
	ringing.Store(true)
	time.Sleep(20 * time.Millisecond)
	n := count()
	time.Sleep(50 * time.Millisecond)
	if count() != n {
		t.Fatalf("rechecked during a ring: %d -> %d", n, count())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range applied {
		if l != 30 {
			t.Fatalf("applied %v", applied)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
