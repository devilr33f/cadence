package presence_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/presence"
)

// a signal for some devices makes those busy, one for every device all of
// them; the most pressing reason wins, and each change is told once per
// device it changed
func TestSignalsCoverTheirDevices(t *testing.T) {
	r := newRig(t)
	r.run(r.s)
	if got := r.busy(r.a); got != "" {
		t.Fatalf("idle: %q", got)
	}
	r.put("work-laptop", presence.ReasonScreenshare, []string{strings.ToUpper(r.a)}, time.Minute)
	if r.busy(r.a) != "screenshare" || r.busy(r.b) != "" {
		t.Fatalf("a %q b %q", r.busy(r.a), r.busy(r.b))
	}
	if got := r.changed(); !slices.Equal(got, []string{r.a}) {
		t.Fatalf("changed %v", got)
	}
	r.put("dashboard", presence.ReasonDND, nil, time.Hour)
	if r.busy(r.a) != "screenshare" || r.busy(r.b) != "dnd" {
		t.Fatalf("a %q b %q", r.busy(r.a), r.busy(r.b))
	}
	if got := r.changed(); !slices.Equal(got, []string{r.b}) {
		t.Fatalf("changed %v", got)
	}
	r.put("phone", presence.ReasonCall, nil, time.Minute)
	if r.busy(r.a) != "call" || r.busy(r.b) != "call" {
		t.Fatalf("a %q b %q", r.busy(r.a), r.busy(r.b))
	}
	if got := r.changed(); !slices.Equal(got, sorted(r.a, r.b)) {
		t.Fatalf("changed %v", got)
	}
	// a heartbeat that changes nothing tells no one
	r.put("phone", presence.ReasonCall, nil, time.Minute)
	if got := r.changed(); len(got) != 0 {
		t.Fatalf("heartbeat changed %v", got)
	}
	if err := r.s.Delete(context.Background(), "phone"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Delete(context.Background(), "dashboard"); err != nil {
		t.Fatal(err)
	}
	if r.busy(r.a) != "screenshare" || r.busy(r.b) != "" {
		t.Fatalf("a %q b %q", r.busy(r.a), r.busy(r.b))
	}
	if err := r.s.Delete(context.Background(), "dashboard"); !errors.Is(err, presence.ErrNotFound) {
		t.Fatalf("delete again: %v", err)
	}
	sigs := r.s.Signals()
	if len(sigs) != 1 || sigs[0].Source != "work-laptop" || !slices.Equal(sigs[0].Devices, []string{r.a}) {
		t.Fatalf("signals %+v", sigs)
	}
}

func TestSignalsAreRefused(t *testing.T) {
	r := newRig(t)
	r.run(r.s)
	ctx := context.Background()
	gone := uuid.NewString()
	for _, c := range []struct {
		source, reason string
		devices        []string
		ttl            time.Duration
		want           error
	}{
		{"ha:sensor.phone", "call", nil, time.Minute, presence.ErrReserved},
		{"Phone", "call", nil, time.Minute, presence.ErrInvalid},
		{"", "call", nil, time.Minute, presence.ErrInvalid},
		{strings.Repeat("a", 65), "call", nil, time.Minute, presence.ErrInvalid},
		{"phone", "sleep", nil, time.Minute, presence.ErrInvalid},
		{"phone", "call", nil, 0, presence.ErrInvalid},
		{"phone", "call", nil, presence.MaxTTL + time.Second, presence.ErrInvalid},
		{"phone", "call", []string{}, time.Minute, presence.ErrInvalid},
		{"phone", "call", []string{gone}, time.Minute, presence.ErrInvalid},
		{"phone", "call", []string{r.a, r.a}, time.Minute, presence.ErrInvalid},
		{"phone", "call", []string{"kitchen"}, time.Minute, presence.ErrInvalid},
	} {
		if _, err := r.s.Put(ctx, c.source, c.reason, c.devices, c.ttl); !errors.Is(err, c.want) {
			t.Errorf("%.20s %s %v %v: %v, want %v", c.source, c.reason, c.devices, c.ttl, err, c.want)
		}
	}
	if err := r.s.Delete(ctx, "ha:sensor.phone"); !errors.Is(err, presence.ErrReserved) {
		t.Fatalf("delete ha: %v", err)
	}
	if len(r.s.Signals()) != 0 || len(r.changed()) != 0 {
		t.Fatal("a refused signal was kept")
	}
}

// a signal ends at its time: busy stops at once, the expiry pass tells the
// listeners and the feed, and the row goes
func TestSignalsExpire(t *testing.T) {
	r := newRig(t)
	feed, unsub := r.bus.Subscribe(func(e events.Event) bool { return e.Type == events.BusyChanged })
	defer unsub()
	r.run(r.s)
	r.put("phone", presence.ReasonCall, []string{r.a}, 30*time.Second)
	if e := <-feed; e.DeviceID != r.a || e.Data != (presence.BusyData{Busy: true, Reason: "call"}) {
		t.Fatalf("event %+v", e)
	}
	r.changed()
	r.clock.Advance(30 * time.Second)
	if got := r.busy(r.a); got != "" {
		t.Fatalf("expired still busy: %q", got)
	}
	r.s.Expire(context.Background())
	if got := r.changed(); !slices.Equal(got, []string{r.a}) {
		t.Fatalf("changed %v", got)
	}
	if e := <-feed; e.DeviceID != r.a || e.Data != (presence.BusyData{}) {
		t.Fatalf("event %+v", e)
	}
	var rows int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM busy_signals`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("%d rows %v", rows, err)
	}
	if !strings.Contains(r.logs.String(), "busy_expired source=phone") {
		t.Fatalf("logs %s", r.logs)
	}
}

// the loop expires a signal at its time on the real clock
func TestRunExpiresOnTime(t *testing.T) {
	r := newRig(t)
	s := presence.New(presence.Deps{Queries: r.q})
	changed := make(chan string, 4)
	s.OnChange(func(id string) { changed <- id })
	stop := s.Run(context.Background())
	defer stop()
	if _, err := s.Put(context.Background(), "phone", presence.ReasonCall, []string{r.a}, time.Second); err != nil {
		t.Fatal(err)
	}
	<-changed
	select {
	case id := <-changed:
		if busy, _ := s.Busy(context.Background(), id); busy || id != r.a {
			t.Fatalf("%s busy %v", id, busy)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never expired")
	}
}

// what was stored survives a restart with its expiry; what expired while the
// server was down is dropped at the load
func TestSignalsSurviveARestart(t *testing.T) {
	r := newRig(t)
	r.run(r.s)
	at := r.put("dashboard", presence.ReasonDND, nil, time.Hour)
	r.put("phone", presence.ReasonCall, []string{r.b}, time.Minute)
	r.clock.Advance(2 * time.Minute)
	r.changed()
	s := r.service()
	r.run(s)
	sigs := s.Signals()
	if len(sigs) != 1 || sigs[0].Source != "dashboard" || sigs[0].Devices != nil || !sigs[0].ExpiresAt.Equal(at) {
		t.Fatalf("loaded %+v", sigs)
	}
	if busy, reason := s.Busy(context.Background(), r.b); !busy || reason != "dnd" {
		t.Fatalf("b %v %q", busy, reason)
	}
	// the load tells the listeners of the devices it made busy
	if got := r.changed(); !slices.Equal(got, sorted(r.a, r.b)) {
		t.Fatalf("changed %v", got)
	}
	var rows int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM busy_signals`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("%d rows %v", rows, err)
	}
}

// the store refuses a signal past a day whatever wrote it
func TestStoreBoundsTheExpiry(t *testing.T) {
	r := newRig(t)
	_, err := r.pool.Exec(context.Background(),
		`INSERT INTO busy_signals (source, reason, expires_at, updated_at) VALUES ('x', 'dnd', now() + interval '25 hours', now())`)
	if err == nil {
		t.Fatal("a signal past a day was stored")
	}
	_, err = r.pool.Exec(context.Background(),
		`INSERT INTO busy_signals (source, reason, expires_at) VALUES ('x', 'nap', now() + interval '1 hour')`)
	if err == nil {
		t.Fatal("an unknown reason was stored")
	}
}

// the presence scope is one the token store takes
func TestTokensTakeThePresenceScope(t *testing.T) {
	r := newRig(t)
	_, err := r.pool.Exec(context.Background(),
		`INSERT INTO api_tokens (name, hash, scopes) VALUES ('mac', '\x01', '{presence}')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.pool.Exec(context.Background(),
		`INSERT INTO api_tokens (name, hash, scopes) VALUES ('other', '\x02', '{busy}')`)
	if err == nil {
		t.Fatal("an unknown scope was stored")
	}
}
