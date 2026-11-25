package ha_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha/hatest"
)

const token = "ha-token-7c1e9b-never-printed"

// fast keeps every wait in the tens of milliseconds
var fast = ha.Timings{
	BackoffMin: 10 * time.Millisecond, BackoffMax: 40 * time.Millisecond, StableAfter: 80 * time.Millisecond,
	Command: time.Second,
}

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *logBuffer) count(event string) int {
	return strings.Count(b.String(), "msg="+event+" ") + strings.Count(b.String(), "msg="+event+"\n")
}

var delayRe = regexp.MustCompile(`msg=ha_reconnect delay_ms=(\d+)`)

func (b *logBuffer) delays() []int {
	var out []int
	for _, m := range delayRe.FindAllStringSubmatch(b.String(), -1) {
		n, _ := strconv.Atoi(m[1])
		out = append(out, n)
	}
	return out
}

type rig struct {
	t    *testing.T
	fake *hatest.Server
	c    *ha.Client
	logs *logBuffer
	log  *slog.Logger
	stop func()
	done chan struct{}
}

type rigOpt func(*rigConfig)

type rigConfig struct {
	token string
	roots *x509.CertPool
	tune  ha.Timings
	base  string
}

func withToken(tok string) rigOpt          { return func(c *rigConfig) { c.token = tok } }
func withTimings(t ha.Timings) rigOpt      { return func(c *rigConfig) { c.tune = t } }
func withBase(suffix string) rigOpt        { return func(c *rigConfig) { c.base = suffix } }
func noRoots() rigOpt                      { return func(c *rigConfig) { c.roots = nil } }
func tuned(f func(*ha.Timings)) ha.Timings { t := fast; f(&t); return t }

// newRig builds a client of a fresh fake; every log line it wrote is checked
// for the token, the fake's text and the fake's address at cleanup.
func newRig(t *testing.T, opts ...rigOpt) *rig {
	t.Helper()
	f := hatest.New(token)
	t.Cleanup(f.Close)
	cfg := rigConfig{token: token, roots: f.CA(), tune: fast}
	for _, o := range opts {
		o(&cfg)
	}
	logs := &logBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	c, err := ha.NewClient(ha.ClientConfig{BaseURL: f.URL() + cfg.base, Token: cfg.token, Roots: cfg.roots, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	ha.SetTimings(c, cfg.tune)
	r := &rig{t: t, fake: f, c: c, logs: logs, log: log, stop: func() {}, done: make(chan struct{})}
	t.Cleanup(func() {
		r.stop()
		host := strings.TrimPrefix(f.URL(), "https://")
		out := logs.String()
		for _, secret := range []string{token, cfg.token, hatest.Leak, host} {
			if strings.Contains(out, secret) {
				t.Errorf("a log line carries %q:\n%s", secret, out)
			}
		}
	})
	return r
}

func (r *rig) run() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(r.done)
		r.c.Run(ctx)
	}()
	r.stop = func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			r.t.Error("Run did not return after its context ended")
		}
	}
}

func (r *rig) waitConnected() {
	r.t.Helper()
	eventually(r.t, "connected", r.c.Connected)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func bg() context.Context { return context.Background() }

// wantCode checks err's code and that its text names nothing upstream.
func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if got := ha.Code(err); got != code {
		t.Fatalf("code %q (%v), want %q", got, err, code)
	}
	for _, secret := range []string{token, hatest.Leak, "https://", "wss://"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q carries %q", err, secret)
		}
	}
}
