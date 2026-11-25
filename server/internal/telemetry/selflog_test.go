package telemetry_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

func TestServerLinesStored(t *testing.T) {
	pool := storetest.Pool(t)
	out := &syncBuffer{}
	tee := telemetry.NewTee(out)
	log := slog.New(tee)
	log.Info("before_attach")
	self := telemetry.NewSelfLog(telemetry.NewLogStore(db.New(pool)), tee.Local())
	stop := self.Run(context.Background())
	tee.Attach(self)
	log.With("device_id", "d1").Info("link_open", "count", 2)
	log.Warn("link_idle")
	log.Error("auth_unavailable")
	log.Debug("not_enabled")
	stop()
	tee.Detach()
	log.Info("after_detach")

	written := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(written) != 5 || !strings.Contains(written[1], `"msg":"link_open","device_id":"d1","count":2`) {
		t.Fatalf("written %q", written)
	}
	rows, err := pool.Query(context.Background(), `SELECT ts, observed_ts, device_id IS NULL, source, severity, body FROM device_logs ORDER BY ts, severity`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	var severities []int16
	for rows.Next() {
		var (
			ts, observed time.Time
			null         bool
			source, body string
			sev          int16
		)
		if err := rows.Scan(&ts, &observed, &null, &source, &sev, &body); err != nil {
			t.Fatal(err)
		}
		if !null || source != "server" || time.Since(ts) > time.Minute || !observed.Equal(ts) {
			t.Fatalf("row %v %v %q %v", ts, null, source, observed)
		}
		got, severities = append(got, body), append(severities, sev)
	}
	// the body is the line as written, without its newline
	if len(got) != 3 || got[0] != written[1] || got[1] != written[2] || got[2] != written[3] {
		t.Fatalf("stored %q", got)
	}
	if severities[0] != 9 || severities[1] != 13 || severities[2] != 17 {
		t.Fatalf("severities %v", severities)
	}
}

// fakeLines stands in for the log store
type fakeLines struct {
	mu      sync.Mutex
	batches [][]telemetry.LogRow
	fail    int
	block   chan struct{}
	// hang holds every insert until its context ends, as a database that went away
	hang  bool
	calls int
}

func (f *fakeLines) Insert(ctx context.Context, rows []telemetry.LogRow) error {
	if f.block != nil {
		<-f.block
	}
	if f.hang {
		<-ctx.Done()
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail > 0 {
		f.fail--
		return errors.New("store down: password=hunter2")
	}
	f.batches = append(f.batches, rows)
	return nil
}

func (f *fakeLines) sizes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, b := range f.batches {
		out = append(out, len(b))
	}
	return out
}

func (f *fakeLines) bodies() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, batch := range f.batches {
		for _, r := range batch {
			b.WriteString(r.Body + "\n")
		}
	}
	return b.String()
}

func TestServerLinesBatched(t *testing.T) {
	store := &fakeLines{}
	tee := telemetry.NewTee(&syncBuffer{})
	self := telemetry.NewSelfLog(store, tee.Local())
	telemetry.SetFlushEvery(self, 100*time.Millisecond)
	tee.Attach(self)
	stop := self.Run(context.Background())
	defer stop()
	log := slog.New(tee)
	for range 1200 {
		log.Info("line")
	}
	// 500 at a time, the rest once a batch waited its interval
	eventually(t, "1200 lines", func() bool {
		s := store.sizes()
		total := 0
		for _, n := range s {
			if n > 500 {
				t.Fatalf("a batch of %d", n)
			}
			total += n
		}
		return total == 1200
	})
	if s := store.sizes(); len(s) != 3 || s[0] != 500 || s[1] != 500 || s[2] != 200 {
		t.Fatalf("batches %v", s)
	}
	if self.Dropped() != 0 {
		t.Fatalf("dropped %d", self.Dropped())
	}
}

// a stalled store never holds a request up: the queue is bounded and its overflow counted
func TestServerLinesNeverBlock(t *testing.T) {
	store := &fakeLines{block: make(chan struct{})}
	tee := telemetry.NewTee(&syncBuffer{})
	self := telemetry.NewSelfLog(store, tee.Local())
	tee.Attach(self)
	stop := self.Run(context.Background())
	log := slog.New(tee)
	began := time.Now()
	for range 20000 {
		log.Info("line")
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("20000 lines took %v", took)
	}
	if self.Dropped() == 0 {
		t.Fatal("nothing dropped")
	}
	close(store.block)
	stop()
}

// the writer's own trouble goes to the local log alone: never into the queue, so it cannot loop
func TestServerLinesTroubleStaysLocal(t *testing.T) {
	store := &fakeLines{fail: 2}
	out := &syncBuffer{}
	tee := telemetry.NewTee(out)
	self := telemetry.NewSelfLog(store, tee.Local())
	telemetry.SetFlushEvery(self, 20*time.Millisecond)
	tee.Attach(self)
	stop := self.Run(context.Background())
	log := slog.New(tee)
	log.Info("first")
	eventually(t, "the failure logged", func() bool { return strings.Contains(out.String(), "server_logs_failed") })
	log.Info("second")
	eventually(t, "the second batch failed", func() bool { return self.Dropped() == 2 })
	log.Info("third")
	eventually(t, "a line stored", func() bool { return strings.Contains(store.bodies(), "third") })
	stop()
	if b := store.bodies(); strings.Contains(b, "server_logs_failed") {
		t.Fatalf("the writer's own line was queued: %s", b)
	}
	l := out.String()
	// one line per outage, not one per failed batch, and no cause
	if strings.Count(l, "server_logs_failed") != 1 || strings.Contains(l, "hunter2") || !strings.Contains(l, `"dropped":1`) {
		t.Fatalf("log %s", l)
	}
	if self.Dropped() != 2 {
		t.Fatalf("dropped %d", self.Dropped())
	}
}

// the database gone: a stop gives the queued lines one store timeout in all,
// not one per batch, and counts what it could not store
func TestServerLinesStopWithTheStoreDown(t *testing.T) {
	store := &fakeLines{hang: true}
	tee := telemetry.NewTee(&syncBuffer{})
	self := telemetry.NewSelfLog(store, tee.Local())
	telemetry.SetFlushEvery(self, 10*time.Millisecond)
	telemetry.SetStoreTimeout(self, 200*time.Millisecond)
	tee.Attach(self)
	stop := self.Run(context.Background())
	log := slog.New(tee)
	for range 4000 {
		log.Info("line")
	}
	began := time.Now()
	stop()
	if took := time.Since(began); took > time.Second {
		t.Fatalf("stop took %v", took)
	}
	if n := self.Dropped(); n != 4000 {
		t.Fatalf("dropped %d", n)
	}
}

// a store that refuses fast: the stop tries one batch of what is queued, not each
func TestServerLinesDrainStopsAtAFailure(t *testing.T) {
	store := &fakeLines{block: make(chan struct{}), fail: 1 << 30}
	tee := telemetry.NewTee(&syncBuffer{})
	self := telemetry.NewSelfLog(store, tee.Local())
	tee.Attach(self)
	stop := self.Run(context.Background())
	log := slog.New(tee)
	// the first 500 go into a call the store holds, the rest wait in the queue
	for range 3600 {
		log.Info("line")
	}
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	// the stop is under way before the held call returns
	time.Sleep(time.Second)
	close(store.block)
	<-stopped
	store.mu.Lock()
	calls := store.calls
	store.mu.Unlock()
	if calls != 2 || self.Dropped() != 3600 {
		t.Fatalf("%d calls, %d dropped", calls, self.Dropped())
	}
}
