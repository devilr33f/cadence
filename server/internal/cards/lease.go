package cards

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// Live cards after a turn (item 5): a turn's ha_control write makes the entity's card on each of the turn's screens
// followable for FollowCap, and a catalog play's card for its own follow time; a screen follows it while it holds a lease on it, renewed by
// heartbeat (cards.lease every LeaseHeartbeat, lapsing after LeaseTTL), released when its panel closes
// (cards.release), when its link drops (cadenced's cards.release all, or the server's link closing) or when the
// lease lapses. While a screen's lease holds a card, the card's next states go to it as Cards frames (context
// "live"), at most 2 a second; with no lease left on it, its follow stops. A lease is idempotent: a screen back
// after a drop acquires again by its next heartbeat. States reach screens only: lines carry device ids, lease
// counts and codes, never an entity or its state (rule 8).
const (
	LeaseHeartbeat = 3 * time.Second
	LeaseTTL       = 8 * time.Second
	FollowCap      = 3 * time.Minute
	// at most 2 revisions a second
	followGap = 500 * time.Millisecond
	// LiveContext is the Cards frames' context
	LiveContext = "live"
	maxLeaseID  = 64
	maxLeases   = 8
)

// LeaseDeps are the leases' collaborators.
type LeaseDeps struct {
	Cards *Service
	// Send puts a frame on a device's link
	Send func(deviceID string, env *cadencev2.Envelope) error
	Now  func() time.Time
	Log  *slog.Logger
	// zero is the package's own bound
	TTL, Cap, Gap time.Duration
}

// Leases keeps each screen's leases and the follows they hold.
type Leases struct {
	d LeaseDeps

	mu sync.Mutex
	// allowed are the cards a turn's write made followable, per device, until their cap
	allowed map[string]map[string]*followable
	leases  map[string]map[string]*lease
	follows map[string]map[string]*follow
}

type followable struct {
	subj  Subject
	until time.Time
}

type lease struct {
	cards   []string
	expires time.Time
}

type follow struct {
	cancel context.CancelFunc
}

func NewLeases(d LeaseDeps) *Leases {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.TTL <= 0 {
		d.TTL = LeaseTTL
	}
	if d.Cap <= 0 {
		d.Cap = FollowCap
	}
	if d.Gap <= 0 {
		d.Gap = followGap
	}
	return &Leases{d: d, allowed: map[string]map[string]*followable{}, leases: map[string]map[string]*lease{},
		follows: map[string]map[string]*follow{}}
}

// Allow makes subj's card on dev followable for the cap from now: the turn wrote to its entity. A subject that is
// not live is never followed.
func (l *Leases) Allow(dev string, subj Subject) {
	if _, ok := subj.(Live); !ok || subj == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.allowed[dev]
	if m == nil {
		m = map[string]*followable{}
		l.allowed[dev] = m
	}
	limit := l.d.Cap
	// a card with a follow time of its own (a catalog play: past a slow torrent's start) keeps it
	if f, ok := subj.(interface{ Follow() time.Duration }); ok && f.Follow() > 0 {
		limit = f.Follow()
	}
	m[subj.ID()] = &followable{subj: subj, until: l.d.Now().Add(limit)}
	l.reconcileLocked(dev)
}

// Lease acquires or renews a screen's lease on the cards it names; it answers how many it follows.
func (l *Leases) Lease(dev, id string, cardIDs []string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.leases[dev]
	if m == nil {
		m = map[string]*lease{}
		l.leases[dev] = m
	}
	if m[id] == nil && len(m) >= maxLeases {
		return 0
	}
	m[id] = &lease{cards: cardIDs, expires: l.d.Now().Add(l.d.TTL)}
	l.reconcileLocked(dev)
	n := 0
	for _, c := range cardIDs {
		if l.follows[dev][c] != nil {
			n++
		}
	}
	return n
}

// Release ends one lease of a screen.
func (l *Leases) Release(dev, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.leases[dev], id)
	l.reconcileLocked(dev)
}

// ReleaseAll ends every lease of a screen: its link dropped. What it may follow stays, so it acquires again once
// back.
func (l *Leases) ReleaseAll(dev string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.leases, dev)
	l.reconcileLocked(dev)
}

// Following is how many devices follow the card now: its entity's subscription holds while it is above zero.
func (l *Leases) Following(cardID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, fs := range l.follows {
		if fs[cardID] != nil {
			n++
		}
	}
	return n
}

// Sweep lapses leases past their ttl and follows past their cap.
func (l *Leases) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.d.Now()
	devs := map[string]bool{}
	for dev, m := range l.leases {
		for id, ls := range m {
			if !now.Before(ls.expires) {
				delete(m, id)
				devs[dev] = true
				l.d.Log.Info("card_lease_lapsed", "device_id", dev)
			}
		}
	}
	for dev, m := range l.allowed {
		for id, f := range m {
			if !now.Before(f.until) {
				delete(m, id)
				devs[dev] = true
			}
		}
	}
	for dev := range devs {
		l.reconcileLocked(dev)
	}
}

// Run sweeps each second until ctx ends.
func (l *Leases) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				l.Sweep()
			}
		}
	}()
	return func() {
		cancel()
		<-done
		l.mu.Lock()
		for dev := range l.follows {
			delete(l.leases, dev)
			l.reconcileLocked(dev)
		}
		l.mu.Unlock()
	}
}

// reconcileLocked starts a follow for each card a live lease of dev holds and the turn allows, and stops the rest
func (l *Leases) reconcileLocked(dev string) {
	now := l.d.Now()
	want := map[string]*followable{}
	for _, ls := range l.leases[dev] {
		if !now.Before(ls.expires) {
			continue
		}
		for _, c := range ls.cards {
			if f := l.allowed[dev][c]; f != nil && now.Before(f.until) {
				want[c] = f
			}
		}
	}
	fs := l.follows[dev]
	for c, f := range fs {
		if want[c] == nil {
			f.cancel()
			delete(fs, c)
			l.d.Log.Info("card_follow_stopped", "device_id", dev, "card_id", c)
		}
	}
	for c, f := range want {
		if fs[c] != nil {
			continue
		}
		if fs == nil {
			fs = map[string]*follow{}
			l.follows[dev] = fs
		}
		ctx, cancel := context.WithDeadline(context.Background(), f.until)
		fl := &follow{cancel: cancel}
		fs[c] = fl
		l.d.Log.Info("card_follow_started", "device_id", dev, "card_id", c)
		go l.run(ctx, dev, c, f.subj, fl)
	}
	if len(fs) == 0 {
		delete(l.follows, dev)
	}
	if len(l.leases[dev]) == 0 {
		delete(l.leases, dev)
	}
}

// run pushes the card's next revisions to dev until ctx ends (no lease left, or the cap)
func (l *Leases) run(ctx context.Context, dev, cardID string, subj Subject, fl *follow) {
	defer func() {
		fl.cancel()
		l.mu.Lock()
		if l.follows[dev][cardID] == fl {
			delete(l.follows[dev], cardID)
			if len(l.follows[dev]) == 0 {
				delete(l.follows, dev)
			}
		}
		l.mu.Unlock()
	}()
	live := subj.(Live)
	var last time.Time
	for ctx.Err() == nil {
		if !live.Changed(ctx) {
			continue
		}
		if wait := l.d.Gap - time.Since(last); !last.IsZero() && wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		c, err := l.d.Cards.Show(ctx, dev, subj)
		if err != nil || ctx.Err() != nil {
			return
		}
		last = time.Now()
		_ = l.d.Send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_Cards{Cards: &cadencev2.Cards{
			ContextId: LiveContext, Cards: []*cardsv1.Card{c},
		}}})
	}
}

// LeaseMethod serves cards.lease {leaseId, cardIds}.
func (l *Leases) LeaseMethod() calls.Method {
	return func(_ context.Context, c calls.Call) calls.Reply {
		var a struct {
			LeaseID string   `json:"leaseId"`
			CardIDs []string `json:"cardIds"`
		}
		if json.Unmarshal(c.Args, &a) != nil || a.LeaseID == "" || len(a.LeaseID) > maxLeaseID || len(a.CardIDs) > 24 {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		n := l.Lease(c.DeviceID, a.LeaseID, a.CardIDs)
		return calls.OK(map[string]int64{"following": int64(n), "ttlMs": l.d.TTL.Milliseconds(),
			"heartbeatMs": (l.d.TTL / 3).Milliseconds()})
	}
}

// ReleaseMethod serves cards.release {leaseId} or {all: true}.
func (l *Leases) ReleaseMethod() calls.Method {
	return func(_ context.Context, c calls.Call) calls.Reply {
		var a struct {
			LeaseID string `json:"leaseId"`
			All     bool   `json:"all"`
		}
		if json.Unmarshal(c.Args, &a) != nil || (a.LeaseID == "" && !a.All) || len(a.LeaseID) > maxLeaseID {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		if a.All {
			l.ReleaseAll(c.DeviceID)
		} else {
			l.Release(c.DeviceID, a.LeaseID)
		}
		return calls.OK(map[string]bool{"released": true})
	}
}
