package presence_test

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/presence"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type rig struct {
	t     *testing.T
	pool  *pgxpool.Pool
	q     *db.Queries
	clock *clock
	bus   *events.Bus
	logs  *syncBuf
	s     *presence.Service
	// a, b are enrolled devices
	a, b string

	mu      sync.Mutex
	changes []string
}

func newRig(t *testing.T) *rig {
	pool := storetest.Pool(t)
	r := &rig{
		t: t, pool: pool, q: db.New(pool), clock: &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)},
		bus: events.NewBus(0), logs: &syncBuf{},
	}
	r.a, r.b = r.device(), r.device()
	r.s = r.service()
	return r
}

// service is a new Service on the rig's store, as after a restart; its
// changes are recorded.
func (r *rig) service() *presence.Service {
	s := presence.New(presence.Deps{
		Queries: r.q, Bus: r.bus, Log: slog.New(slog.NewTextHandler(r.logs, nil)), Now: r.clock.Now,
	})
	s.OnChange(func(id string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.changes = append(r.changes, id)
	})
	return s
}

func (r *rig) device() string {
	r.t.Helper()
	var id string
	err := r.pool.QueryRow(context.Background(),
		`INSERT INTO devices (name, model, status) VALUES (gen_random_uuid()::text, 'sim', 'enrolled') RETURNING id::text`).Scan(&id)
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

func (r *rig) run(s *presence.Service) {
	stop := s.Run(context.Background())
	r.t.Cleanup(stop)
}

// changed returns the devices told of since the last call, sorted.
func (r *rig) changed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.changes
	r.changes = nil
	slices.Sort(out)
	return out
}

func (r *rig) busy(id string) string {
	busy, reason := r.s.Busy(context.Background(), id)
	if busy != (reason != "") {
		r.t.Fatalf("busy %v with reason %q", busy, reason)
	}
	return reason
}

func (r *rig) put(source, reason string, devices []string, ttl time.Duration) time.Time {
	r.t.Helper()
	at, err := r.s.Put(context.Background(), source, reason, devices, ttl)
	if err != nil {
		r.t.Fatalf("put %s: %v", source, err)
	}
	return at
}

func sorted(ids ...string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func noEntity(t *testing.T, logs string) {
	t.Helper()
	if strings.Contains(logs, "sensor.") || strings.Contains(logs, "binary_sensor") {
		t.Fatalf("an entity id in the logs: %s", logs)
	}
}
