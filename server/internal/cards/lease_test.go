package cards_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// heater is a live entity whose state the test sets; every set wakes each card that follows it
type heater struct {
	mu    sync.Mutex
	state string
	wake  chan struct{}
}

func newHeater() *heater { return &heater{state: "Нагрев 40 °C", wake: make(chan struct{})} }

func (h *heater) set(s string) {
	h.mu.Lock()
	h.state = s
	close(h.wake)
	h.wake = make(chan struct{})
	h.mu.Unlock()
}

// subject is one screen's card of the heater, as cards.HADeps.Entity answers one per screen
func (h *heater) subject() cards.Subject { return &heaterCard{h: h} }

type heaterCard struct {
	h    *heater
	mu   sync.Mutex
	last string
}

func (c *heaterCard) ID() string { return "ha-heater" }

func (c *heaterCard) Changed(ctx context.Context) bool {
	for {
		c.h.mu.Lock()
		now, wake := c.h.state, c.h.wake
		c.h.mu.Unlock()
		c.mu.Lock()
		last := c.last
		c.mu.Unlock()
		if now != last {
			return true
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return false
		}
	}
}

func (c *heaterCard) Render(context.Context, *cards.Render) (*cardsv1.Card, error) {
	c.h.mu.Lock()
	s := c.h.state
	c.h.mu.Unlock()
	c.mu.Lock()
	c.last = s
	c.mu.Unlock()
	return pcards.NewCard("", 0, s, &cardsv1.CardBlock{Title: "Бойлер", Children: pcards.Nodes(&cardsv1.State{Label: s, Kind: cardsv1.StateKind_STATE_KIND_ON})}), nil
}

type leaseRig struct {
	t      *testing.T
	l      *cards.Leases
	svc    *cards.Service
	logs   *bytes.Buffer
	mu     sync.Mutex
	now    time.Time
	pushed map[string][]*cardsv1.Card
	at     map[string][]time.Time
}

func newLeaseRig(t *testing.T) *leaseRig {
	r := &leaseRig{t: t, logs: &bytes.Buffer{}, now: time.Now(), pushed: map[string][]*cardsv1.Card{}, at: map[string][]time.Time{}}
	clock := func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }
	log := slog.New(slog.NewTextHandler(&syncWriter{mu: &r.mu, w: r.logs}, nil))
	r.svc = cards.New(cards.Deps{Now: clock, Log: log})
	r.l = cards.NewLeases(cards.LeaseDeps{Cards: r.svc, Now: clock, Log: log, Gap: 100 * time.Millisecond,
		Send: func(dev string, env *cadencev2.Envelope) error {
			c := env.GetCards()
			if c.GetContextId() != cards.LiveContext {
				t.Errorf("context %q", c.GetContextId())
			}
			r.mu.Lock()
			r.pushed[dev] = append(r.pushed[dev], c.GetCards()...)
			r.at[dev] = append(r.at[dev], time.Now())
			r.mu.Unlock()
			return nil
		}})
	t.Cleanup(r.l.Run(context.Background()))
	return r
}

type syncWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (r *leaseRig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
	r.l.Sweep()
}

// show issues the heater's card on a screen and lets it follow, as a turn's write does
func (r *leaseRig) show(dev string, h *heater) {
	subj := h.subject()
	if _, err := r.svc.Show(context.Background(), dev, subj); err != nil {
		r.t.Fatal(err)
	}
	r.l.Allow(dev, subj)
}

func (r *leaseRig) last(dev string) (string, uint32, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cs := r.pushed[dev]
	if len(cs) == 0 {
		return "", 0, 0
	}
	c := cs[len(cs)-1]
	return c.GetRoot().GetCard().GetChildren()[0].GetState().GetLabel(), c.GetRevision(), len(cs)
}

func (r *leaseRig) await(dev, state string) uint32 {
	r.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s, rev, _ := r.last(dev); s == state {
			return rev
		}
		time.Sleep(5 * time.Millisecond)
	}
	s, _, _ := r.last(dev)
	r.t.Fatalf("%s never showed %q (last %q)", dev, state, s)
	return 0
}

func (r *leaseRig) following(want int) {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.l.Following("ha-heater") != want {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.l.Following("ha-heater"); got != want {
		r.t.Fatalf("following %d, want %d", got, want)
	}
}

// a leased card follows its entity, at most 2 revisions a second; a card no write touched is never followed, and
// no state reaches a log
func TestLeaseFollowsAWrittenEntity(t *testing.T) {
	r := newLeaseRig(t)
	h := newHeater()
	if n := r.l.Lease("tv", "L1", []string{"ha-heater"}); n != 0 {
		t.Fatalf("following %d before any write", n)
	}
	r.show("tv", h)
	if n := r.l.Lease("tv", "L1", []string{"ha-heater"}); n != 1 {
		t.Fatalf("following %d", n)
	}
	h.set("Нагрев 45 °C")
	r.await("tv", "Нагрев 45 °C")
	for i := range 5 {
		h.set("Нагрев " + string(rune('A'+i)))
		time.Sleep(10 * time.Millisecond)
	}
	r.await("tv", "Нагрев E")
	r.mu.Lock()
	at := r.at["tv"]
	r.mu.Unlock()
	if len(at) > 4 {
		t.Fatalf("%d pushes for 6 changes in 50 ms: not throttled", len(at))
	}
	for i := 1; i < len(at); i++ {
		if gap := at[i].Sub(at[i-1]); gap < 95*time.Millisecond {
			t.Fatalf("pushes %v apart, want the gap", gap)
		}
	}
	if strings.Contains(r.logs.String(), "Нагрев") {
		t.Fatalf("a state in the logs:\n%s", r.logs)
	}
}

// a lease the screen stops renewing lapses with no close: its follow stops
func TestLeaseLapsesWithoutAClose(t *testing.T) {
	r := newLeaseRig(t)
	h := newHeater()
	r.show("tv", h)
	r.l.Lease("tv", "L1", []string{"ha-heater"})
	r.following(1)
	r.advance(cards.LeaseHeartbeat)
	r.l.Lease("tv", "L1", []string{"ha-heater"})
	r.advance(cards.LeaseHeartbeat)
	r.following(1)
	r.advance(cards.LeaseTTL)
	r.following(0)
	h.set("Выключен")
	time.Sleep(50 * time.Millisecond)
	if s, _, _ := r.last("tv"); s == "Выключен" {
		t.Fatal("a lapsed lease still pushed")
	}
}

// a dropped link releases every lease of the screen; back, its next heartbeat acquires again and the card goes on
// from where it was on screen
func TestLinkDropReleasesAndReconnectReacquires(t *testing.T) {
	r := newLeaseRig(t)
	h := newHeater()
	r.show("tv", h)
	r.l.Lease("tv", "L1", []string{"ha-heater"})
	h.set("Нагрев 50 °C")
	before := r.await("tv", "Нагрев 50 °C")
	r.l.ReleaseAll("tv")
	r.svc.Forget("tv")
	r.following(0)
	h.set("Нагрев 55 °C")
	if n := r.l.Lease("tv", "L1", []string{"ha-heater"}); n != 1 {
		t.Fatalf("re-acquired %d", n)
	}
	if rev := r.await("tv", "Нагрев 55 °C"); rev <= before {
		t.Fatalf("revision %d after %d: the screen would keep the old one", rev, before)
	}
	// the release over rpc, as cadenced sends it for a screen gone from its socket
	rep := r.l.ReleaseMethod()(context.Background(), calls.Call{DeviceID: "tv", ID: "x", Method: rpc.CardsRelease, Args: []byte(`{"all":true}`)})
	if rep.Status != rpc.StatusOK {
		t.Fatalf("release %v", rep)
	}
	r.following(0)
}

// two screens follow the same entity; one closing its panel leaves the other following
func TestTwoScreensOneCloses(t *testing.T) {
	r := newLeaseRig(t)
	h := newHeater()
	r.show("tv", h)
	r.show("tv2", h)
	r.l.Lease("tv", "A", []string{"ha-heater"})
	rep := r.l.LeaseMethod()(context.Background(), calls.Call{DeviceID: "tv2", ID: "y", Method: rpc.CardsLease,
		Args: []byte(`{"leaseId":"B","cardIds":["ha-heater"]}`)})
	var ans struct{ Following, TTLMs, HeartbeatMs int64 }
	b, _ := json.Marshal(rep.JSON)
	_ = json.Unmarshal(b, &ans)
	if rep.Status != rpc.StatusOK || ans.Following != 1 || ans.HeartbeatMs*2 > ans.TTLMs {
		t.Fatalf("lease %v %+v", rep, ans)
	}
	r.following(2)
	r.l.Release("tv", "A")
	r.following(1)
	h.set("Нагрев 60 °C")
	r.await("tv2", "Нагрев 60 °C")
	time.Sleep(50 * time.Millisecond)
	if s, _, _ := r.last("tv"); s == "Нагрев 60 °C" {
		t.Fatal("the closed panel's screen still follows")
	}
	r.l.Release("tv2", "B")
	r.following(0)
}

// a few minutes past the write the follow stops, renewed or not, and a later lease does not start it again
func TestFollowHardCap(t *testing.T) {
	r := newLeaseRig(t)
	h := newHeater()
	r.show("tv", h)
	for range int(cards.FollowCap / cards.LeaseHeartbeat) {
		r.l.Lease("tv", "L1", []string{"ha-heater"})
		r.advance(cards.LeaseHeartbeat)
	}
	r.advance(cards.LeaseHeartbeat)
	if n := r.l.Lease("tv", "L1", []string{"ha-heater"}); n != 0 {
		t.Fatalf("following %d past the cap", n)
	}
	r.following(0)
	// a new write follows again
	r.show("tv", h)
	if n := r.l.Lease("tv", "L1", []string{"ha-heater"}); n != 1 {
		t.Fatalf("following %d after a new write", n)
	}
}
