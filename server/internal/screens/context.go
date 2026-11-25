package screens

import (
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// ContextTTL is how long a screen's last context stays (W0-C item 13, D11).
const ContextTTL = 10 * time.Minute

// maxVisible bounds the refs a context keeps, as the device's contract does
const maxVisible = 24

// Contexts keeps each screen's latest ScreenContext in memory, 10 minutes at
// most. What it holds (refs, the search query, the player's item) is
// private: never logged, never stored, read by the model's notes alone
// (stream O builds them).
type Contexts struct {
	now func() time.Time

	mu   sync.Mutex
	byID map[string]heldContext
}

type heldContext struct {
	ctx *cadencev2.ScreenContext
	at  time.Time
}

// NewContexts builds an empty store; now nil is time.Now.
func NewContexts(now func() time.Time) *Contexts {
	if now == nil {
		now = time.Now
	}
	return &Contexts{now: now, byID: map[string]heldContext{}}
}

// Put keeps c as the screen's context unless it is older than the one held
// (a lower or equal revision). It answers whether c was kept.
func (s *Contexts) Put(deviceID string, c *cadencev2.ScreenContext) bool {
	if c == nil {
		return false
	}
	c = proto.Clone(c).(*cadencev2.ScreenContext)
	if len(c.VisibleRefs) > maxVisible {
		c.VisibleRefs = c.VisibleRefs[:maxVisible]
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.byID[deviceID]; ok && now.Sub(cur.at) < ContextTTL && c.GetRevision() <= cur.ctx.GetRevision() {
		return false
	}
	s.byID[deviceID] = heldContext{ctx: c, at: now}
	return true
}

// Get is the screen's context while it is fresh; the caller must not change it.
func (s *Contexts) Get(deviceID string) (*cadencev2.ScreenContext, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.byID[deviceID]
	if !ok {
		return nil, false
	}
	if s.now().Sub(cur.at) >= ContextTTL {
		delete(s.byID, deviceID)
		return nil, false
	}
	return cur.ctx, true
}

// Forget drops a screen's context: it was deleted, or its link closed and
// the device's next hello starts a new revision count.
func (s *Contexts) Forget(deviceID string) {
	s.mu.Lock()
	delete(s.byID, deviceID)
	s.mu.Unlock()
}

// Sweep drops every context past its ttl.
func (s *Contexts) Sweep() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, cur := range s.byID {
		if now.Sub(cur.at) >= ContextTTL {
			delete(s.byID, id)
		}
	}
}

// Seen is when the screen last sent a context (a route change, a focus move: the user's latest input there), zero
// when none is fresh.
func (s *Contexts) Seen(deviceID string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.byID[deviceID]
	if !ok || s.now().Sub(cur.at) >= ContextTTL {
		return time.Time{}
	}
	return cur.at
}
