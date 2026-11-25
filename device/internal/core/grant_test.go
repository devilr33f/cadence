package core_test

import (
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// localSessions is the target's own record: it knows none of the caller's sessions
type localSessions map[string]cadencev2.Trigger

func (s localSessions) Trigger(id string) (cadencev2.Trigger, bool) {
	t, ok := s[id]
	return t, ok
}

// TV plan item 14: a woken session on speaker A raises the level of TV B through the server's grant, for that one
// command alone; every grant that is not exactly that command's leaves B's own rules, so the raise is refused and a
// lowering still goes
func TestASessionGrantRaisesTheLevelOnceForItsCommand(t *testing.T) {
	now := time.UnixMilli(1_794_700_000_000)
	vol := volume.NewSetter(volume.NewStore(t.TempDir()), 30, slog.New(slog.DiscardHandler))
	out := volume.NewOutput(vol, localSessions{"local": cadencev2.Trigger_TRIGGER_WAKE}, 1, 64000)
	ls, buf := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{out}}, openStore(t),
		core.WithDeviceID("dev-b"), func(s *core.Sync) { core.SetGrantClock(s, func() time.Time { return now }) })
	p := ls.accept(t)
	p.handshake(false)

	grant := func(id string) *cadencev2.SessionGrant {
		return &cadencev2.SessionGrant{
			Id: id, CallerDeviceId: "dev-a", CallerSessionId: "sess-a", CallerTrigger: cadencev2.Trigger_TRIGGER_WAKE,
			TargetDeviceId: "dev-b", Capability: "audio.output", Command: "set_level",
			ExpiresAtUnixMs: now.Add(30 * time.Second).UnixMilli(),
		}
	}
	n := 0
	send := func(name, args string, g *cadencev2.SessionGrant) *cadencev2.CommandResult {
		t.Helper()
		n++
		p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityCommand{CapabilityCommand: &cadencev2.CapabilityCommand{
			RequestId: "r" + strconv.Itoa(n), Capability: "audio.output", Name: name, Json: args, Grant: g,
		}}})
		// the level's state deltas come between the results
		for {
			if r := p.recv().GetCommandResult(); r != nil {
				return r
			}
		}
	}
	raise := func(level string) string { return `{"level":` + level + `,"sessionId":"sess-a"}` }

	refused := map[string]*cadencev2.SessionGrant{}
	for name, mutate := range map[string]func(g *cadencev2.SessionGrant){
		"follow-up":       func(g *cadencev2.SessionGrant) { g.CallerTrigger = cadencev2.Trigger_TRIGGER_FOLLOW_UP },
		"event":           func(g *cadencev2.SessionGrant) { g.CallerTrigger = cadencev2.Trigger_TRIGGER_EVENT },
		"expired":         func(g *cadencev2.SessionGrant) { g.ExpiresAtUnixMs = now.Add(-time.Millisecond).UnixMilli() },
		"long-lived":      func(g *cadencev2.SessionGrant) { g.ExpiresAtUnixMs = now.Add(time.Hour).UnixMilli() },
		"another command": func(g *cadencev2.SessionGrant) { g.Command = "change_level" },
		"another cap":     func(g *cadencev2.SessionGrant) { g.Capability = "alarms" },
		"another target":  func(g *cadencev2.SessionGrant) { g.TargetDeviceId = "dev-c" },
		"its own device":  func(g *cadencev2.SessionGrant) { g.CallerDeviceId = "dev-b" },
		"another session": func(g *cadencev2.SessionGrant) { g.CallerSessionId = "sess-x" },
		"no id":           func(g *cadencev2.SessionGrant) { g.Id = "" },
	} {
		g := grant("g-" + strings.ReplaceAll(name, " ", "-"))
		mutate(g)
		refused[name] = g
	}
	for name, g := range refused {
		if r := send("set_level", raise("60"), g); r.GetCode() != "wake_required" {
			t.Fatalf("%s: raise = %v, want wake_required", name, r)
		}
	}
	if r := send("set_level", raise("60"), nil); r.GetCode() != "wake_required" {
		t.Fatalf("raise without a grant = %v", r)
	}
	if r := send("set_level", raise("60"), grant("g-1")); r.GetCode() != "" || r.GetJson() != `{"level":60}` {
		t.Fatalf("granted raise = %v", r)
	}
	if r := send("set_level", raise("70"), grant("g-1")); r.GetCode() != "wake_required" {
		t.Fatalf("replayed grant = %v, want wake_required", r)
	}
	if r := send("set_level", raise("20"), nil); r.GetCode() != "" || r.GetJson() != `{"level":20}` {
		t.Fatalf("lowering without a grant = %v", r)
	}
	if r := send("change_level", `{"delta":10,"sessionId":"local"}`, nil); r.GetCode() != "" || r.GetJson() != `{"level":30}` {
		t.Fatalf("a local woken raise = %v", r)
	}
	out2 := buf.String()
	for _, want := range []string{`"reason":"replayed"`, `"reason":"expired"`, `"reason":"trigger"`, `"reason":"target"`, `"reason":"command"`, `"reason":"lifetime"`, `"granted":true`} {
		if !strings.Contains(out2, want) {
			t.Fatalf("log lacks %s:\n%s", want, out2)
		}
	}
	if strings.Contains(out2, "sess-a") || strings.Contains(out2, "g-1") {
		t.Fatalf("log carries a grant's ids: %s", out2)
	}
}
