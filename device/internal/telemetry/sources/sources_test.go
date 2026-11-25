package sources_test

import (
	"slices"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
)

// recorder keeps what a source emits
type recorder struct {
	mu   sync.Mutex
	recs []logs.Record
}

func (r *recorder) Emit(rec logs.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
}

func (r *recorder) all() []logs.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.recs)
}

func (r *recorder) bodies() []string {
	var out []string
	for _, rec := range r.all() {
		out = append(out, rec.Body)
	}
	return out
}

// waitFor polls until cond holds, for at most 5 s
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitBodies waits until the source emitted exactly want, in order
func waitBodies(t *testing.T, r *recorder, want ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := r.bodies()
		if slices.Equal(got, want) {
			return
		}
		if len(got) > len(want) || time.Now().After(deadline) {
			t.Fatalf("bodies = %q, want %q", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
