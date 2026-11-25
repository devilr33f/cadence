package core

import (
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const (
	// a grant that claims to live longer is refused: the server sends it at once, and its command waits 10 s
	grantMaxLifetime = time.Minute
	// ids remembered until their grant expires; with 8 commands in flight a link never comes close
	grantsKept   = 256
	maxGrantText = 63
)

// grants checks the server's session grants (TV plan item 14): a command on this device for a woken session of
// another device. A grant is used once, for its target, capability and command alone, before its expiry
type grants struct {
	mu   sync.Mutex
	used map[string]time.Time // id -> expiry
	now  func() time.Time
}

func newGrants() *grants { return &grants{used: map[string]time.Time{}, now: time.Now} }

// admit is the session cc's grant vouches for, or why it vouches for none ("" with no grant at all). a refused grant
// leaves the command to the provider's own rules, so a lowering still goes
func (g *grants) admit(self string, cc *cadencev2.CapabilityCommand) (capability.Vouched, string) {
	gr := cc.GetGrant()
	if gr == nil {
		return capability.Vouched{}, ""
	}
	now := g.now()
	exp := time.UnixMilli(gr.GetExpiresAtUnixMs())
	switch {
	case !text(gr.GetId()) || !text(gr.GetCallerSessionId()) || !text(gr.GetCallerDeviceId()):
		return capability.Vouched{}, "malformed"
	case self == "" || gr.GetTargetDeviceId() != self || gr.GetCallerDeviceId() == self:
		return capability.Vouched{}, "target"
	case gr.GetCapability() != cc.GetCapability() || gr.GetCommand() != cc.GetName():
		return capability.Vouched{}, "command"
	case gr.GetCallerTrigger() != cadencev2.Trigger_TRIGGER_WAKE && gr.GetCallerTrigger() != cadencev2.Trigger_TRIGGER_BUTTON:
		return capability.Vouched{}, "trigger"
	case !now.Before(exp):
		return capability.Vouched{}, "expired"
	case exp.Sub(now) > grantMaxLifetime:
		return capability.Vouched{}, "lifetime"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, e := range g.used {
		if !now.Before(e) {
			delete(g.used, id)
		}
	}
	if _, ok := g.used[gr.GetId()]; ok {
		return capability.Vouched{}, "replayed"
	}
	if len(g.used) >= grantsKept {
		return capability.Vouched{}, "full"
	}
	g.used[gr.GetId()] = exp
	return capability.Vouched{
		CallerDeviceID: gr.GetCallerDeviceId(), SessionID: gr.GetCallerSessionId(), Trigger: gr.GetCallerTrigger(),
	}, "ok"
}

func text(s string) bool { return s != "" && len(s) <= maxGrantText }
