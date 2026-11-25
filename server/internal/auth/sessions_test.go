package auth_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

// clock is a fake now that a test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// store is the sessions of one database whose owner has a password set at
// passwordAt.
type store struct {
	s          *auth.Sessions
	c          *clock
	pool       *pgxpool.Pool
	passwordAt time.Time
}

func newStore(t *testing.T) *store {
	t.Helper()
	pool := storetest.Pool(t)
	c := newClock()
	r := &store{s: auth.NewSessions(pool, c.Now), c: c, pool: pool}
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO admin_users (id, password_hash, updated_at) VALUES (1, 'x', $1) RETURNING updated_at", c.Now()).Scan(&r.passwordAt); err != nil {
		t.Fatal(err)
	}
	return r
}

// open logs in as a check of the current password would.
func (r *store) open(t *testing.T, oldBrowser string) auth.Grant {
	t.Helper()
	g, err := r.s.Open(context.Background(), r.passwordAt, oldBrowser)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func (r *store) valid(t *testing.T, token string) bool {
	t.Helper()
	_, err := r.s.Validate(context.Background(), token)
	if err != nil && !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal(err)
	}
	return err == nil
}

func (r *store) known(t *testing.T, browser string) bool {
	t.Helper()
	ok, err := r.s.KnownBrowser(context.Background(), browser)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func (r *store) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (r *store) lastSeen(t *testing.T, token string) time.Time {
	t.Helper()
	var at time.Time
	if err := r.pool.QueryRow(context.Background(), "SELECT last_seen_at FROM admin_sessions WHERE token_hash = $1", auth.Hash(token)).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at.UTC()
}

var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func TestSessionOpenAndValidate(t *testing.T) {
	ctx := context.Background()
	r := newStore(t)
	g := r.open(t, "")
	sess := g.Session
	if !tokenShape.MatchString(sess.Token) || !tokenShape.MatchString(g.Browser) || g.Browser == sess.Token {
		t.Fatalf("token shapes: %d, %d chars", len(sess.Token), len(g.Browser))
	}
	if !sess.ExpiresAt.Equal(r.c.Now().Add(30 * 24 * time.Hour)) {
		t.Fatalf("expires %v", sess.ExpiresAt)
	}
	got, err := r.s.Validate(ctx, sess.Token)
	if err != nil || !got.ExpiresAt.Equal(sess.ExpiresAt) || got.Token != "" {
		t.Fatalf("validate: %+v %v", got, err)
	}
	// only the tokens' sha256 are stored
	var n int
	if err := r.pool.QueryRow(ctx, "SELECT count(*) FROM admin_sessions WHERE token_hash = $1", auth.Hash(sess.Token)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stored sessions %d %v", n, err)
	}
	if err := r.pool.QueryRow(ctx, "SELECT count(*) FROM admin_browsers WHERE token_hash = $1", auth.Hash(g.Browser)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stored browsers %d %v", n, err)
	}
	other := r.open(t, "")
	if other.Session.Token == sess.Token || other.Browser == g.Browser {
		t.Fatal("same token twice")
	}
	for _, bad := range []string{"", "x", sess.Token + "A", strings.Repeat("A", 43), g.Browser} {
		if r.valid(t, bad) {
			t.Fatalf("%q validated", bad)
		}
	}
}

// a session opens only for the password that was checked: a change between
// the check and the open leaves no session behind
func TestSessionOpensOnlyForTheCheckedPassword(t *testing.T) {
	ctx := context.Background()
	r := newStore(t)
	if _, err := r.pool.Exec(ctx, "UPDATE admin_users SET updated_at = updated_at + interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Open(ctx, r.passwordAt, ""); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("stale check: %v", err)
	}
	if r.count(t, "admin_sessions") != 0 || r.count(t, "admin_browsers") != 0 {
		t.Fatal("a stale check left rows")
	}
}

// each login replaces the known-browser token it came with; a year is its life
func TestKnownBrowserRotatesAndExpires(t *testing.T) {
	r := newStore(t)
	first := r.open(t, "")
	if !r.known(t, first.Browser) {
		t.Fatal("new browser token unknown")
	}
	second := r.open(t, first.Browser)
	if r.known(t, first.Browser) || !r.known(t, second.Browser) {
		t.Fatal("the next login did not replace the token")
	}
	for _, bad := range []string{"", "x", second.Session.Token, strings.Repeat("A", 43)} {
		if r.known(t, bad) {
			t.Fatalf("%q known", bad)
		}
	}
	r.c.Add(365*24*time.Hour - time.Second)
	if !r.known(t, second.Browser) {
		t.Fatal("ended before a year")
	}
	r.c.Add(time.Second)
	if r.known(t, second.Browser) {
		t.Fatal("outlived a year")
	}
}

func TestSessionIdleExpiry(t *testing.T) {
	r := newStore(t)
	idle := r.open(t, "").Session
	used := r.open(t, "").Session
	r.c.Add(6 * 24 * time.Hour)
	if !r.valid(t, used.Token) {
		t.Fatal("used session ended early")
	}
	r.c.Add(24*time.Hour + time.Second)
	if r.valid(t, idle.Token) {
		t.Fatal("idle session outlived 7 days")
	}
	if !r.valid(t, used.Token) {
		t.Fatal("a session seen a day ago ended")
	}
}

func TestSessionAbsoluteExpiry(t *testing.T) {
	r := newStore(t)
	sess := r.open(t, "").Session
	for range 4 {
		r.c.Add(7*24*time.Hour - time.Minute)
		if !r.valid(t, sess.Token) {
			t.Fatal("active session ended before 30 days")
		}
	}
	r.c.Add(2*24*time.Hour + 5*time.Minute)
	if r.valid(t, sess.Token) {
		t.Fatal("session outlived 30 days")
	}
}

// a request bumps last_seen at most once a minute
func TestSessionTouchedAtMostOnceAMinute(t *testing.T) {
	r := newStore(t)
	sess := r.open(t, "").Session
	start := r.c.Now()
	r.c.Add(30 * time.Second)
	r.valid(t, sess.Token)
	if got := r.lastSeen(t, sess.Token); !got.Equal(start) {
		t.Fatalf("touched after 30 s: %v", got)
	}
	r.c.Add(31 * time.Second)
	r.valid(t, sess.Token)
	if got := r.lastSeen(t, sess.Token); !got.Equal(r.c.Now()) {
		t.Fatalf("not touched after 61 s: %v", got)
	}
}

func TestSessionRevoke(t *testing.T) {
	ctx := context.Background()
	r := newStore(t)
	g := r.open(t, "")
	keep := r.open(t, "").Session
	if err := r.s.Revoke(ctx, g.Session.Token); err != nil {
		t.Fatal(err)
	}
	if r.valid(t, g.Session.Token) || !r.valid(t, keep.Token) {
		t.Fatal("revoke ended the wrong session")
	}
	if !r.known(t, g.Browser) {
		t.Fatal("logout forgot the browser")
	}
	if err := r.s.Revoke(ctx, g.Session.Token); err != nil {
		t.Fatal("second revoke:", err)
	}
	if err := r.s.Revoke(ctx, "unknown"); err != nil {
		t.Fatal("unknown token:", err)
	}
}

func TestSessionPrune(t *testing.T) {
	ctx := context.Background()
	r := newStore(t)
	revoked := r.open(t, "")
	_ = r.s.Revoke(ctx, revoked.Session.Token)
	r.open(t, revoked.Browser) // its session goes idle; the old browser is revoked
	live := r.open(t, "")
	r.c.Add(6 * 24 * time.Hour)
	r.valid(t, live.Session.Token)
	r.c.Add(2 * 24 * time.Hour)
	n, err := r.s.Prune(ctx)
	if err != nil || n != 3 {
		t.Fatalf("pruned %d %v", n, err)
	}
	if r.count(t, "admin_sessions") != 1 || r.count(t, "admin_browsers") != 2 || !r.valid(t, live.Session.Token) {
		t.Fatalf("left %d sessions, %d browsers", r.count(t, "admin_sessions"), r.count(t, "admin_browsers"))
	}
}

// lockedBuffer is a log sink a test reads while a goroutine writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestSessionRetentionPrunesAtStart(t *testing.T) {
	ctx := context.Background()
	r := newStore(t)
	g := r.open(t, "")
	_ = r.s.Revoke(ctx, g.Session.Token)
	logs := &lockedBuffer{}
	stop := r.s.RunRetention(ctx, slog.New(slog.NewTextHandler(logs, nil)), time.Hour)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "admin_sessions_pruned") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if r.count(t, "admin_sessions") != 0 || !strings.Contains(logs.String(), "count=1") {
		t.Fatalf("left %d, log %s", r.count(t, "admin_sessions"), logs.String())
	}
}
