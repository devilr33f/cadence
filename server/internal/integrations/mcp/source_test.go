package mcp_test

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
)

// every tool starts Write, Untrusted, opt-in and woken-only (Rulings
// M5d-R1, R3); each override the owner sets flips its own field, but only
// read and trusted together make a tool's output trusted (Ruling M5d-R8)
func TestSourceToolsFollowTheOwnersOverrides(t *testing.T) {
	r := newTools(t, mcptest.Options{}, withTimeout(3*time.Second))
	r.start("echo")
	before := r.src.Tools()
	echo, e := find(t, before, "mcp_srv_echo"), r.entry("srv", "echo")
	if echo.Effects != agent.Write || echo.Output != agent.Untrusted || !echo.DefaultOff || !echo.RequiresWokenSession ||
		echo.EventTurns || echo.TaintExempt || echo.SilentOnSuccess || echo.Timeout != 3*time.Second || echo.Run == nil {
		t.Fatalf("defaults %+v", echo)
	}
	if echo.Name != e.Local || echo.Description != e.Description || echo.Input != e.Tool.Input {
		t.Fatalf("tool %+v, entry %+v", echo, e)
	}
	cases := []struct {
		remote string
		p      mcp.Patch
		want   func(agent.Tool) bool
	}{
		{"fail", asRead, func(tl agent.Tool) bool {
			return tl.Effects == agent.Read && !tl.RequiresWokenSession && tl.Output == agent.Untrusted && tl.DefaultOff
		}},
		{"huge", asTrusted, func(tl agent.Tool) bool {
			return tl.Output == agent.Untrusted && tl.Effects == agent.Write && tl.RequiresWokenSession && tl.DefaultOff
		}},
		{"echo", mcp.Patch{Effects: str(mcp.EffectsRead), Trust: str(mcp.TrustTrusted)}, func(tl agent.Tool) bool {
			return tl.Output == agent.Trusted && tl.Effects == agent.Read && !tl.RequiresWokenSession && tl.DefaultOff
		}},
		{"image", mcp.Patch{EnabledDefault: yes()}, func(tl agent.Tool) bool {
			return !tl.DefaultOff && tl.Effects == agent.Write && tl.RequiresWokenSession && tl.Output == agent.Untrusted
		}},
		{"slow", mcp.Patch{AnyTurn: yes()}, func(tl agent.Tool) bool {
			return !tl.RequiresWokenSession && tl.Effects == agent.Write && tl.DefaultOff && tl.Output == agent.Untrusted
		}},
	}
	for _, c := range cases {
		r.set(c.remote, c.p)
	}
	after := r.src.Tools()
	for _, c := range cases {
		if tl := find(t, after, "mcp_srv_"+c.remote); !c.want(tl) || tl.EventTurns || tl.TaintExempt || tl.Run == nil {
			t.Errorf("%s: %+v", c.remote, tl)
		}
		// a turn that began before keeps what it had
		if tl := find(t, before, "mcp_srv_"+c.remote); tl.Effects != agent.Write || tl.Output != agent.Untrusted ||
			!tl.DefaultOff || !tl.RequiresWokenSession {
			t.Errorf("%s changed in an earlier snapshot: %+v", c.remote, tl)
		}
	}
}

// a changed tool (Ruling M5d-R2) and a withdrawn server's tools reach no turn
func TestSourceLeavesOutChangedAndWithdrawnTools(t *testing.T) {
	r := newTools(t, mcptest.Options{})
	r.start("echo")
	all := []string{"mcp_srv_echo", "mcp_srv_fail", "mcp_srv_huge", "mcp_srv_image", "mcp_srv_slow"}
	if got := toolNamesOf(r.src.Tools()); !slices.Equal(got, all) {
		t.Fatalf("tools %v", got)
	}
	r.f.Add("echo", "Echoes, and more.")
	r.cat.Refresh("srv")
	eventually(t, "echo changed", func() bool { return r.entry("srv", "echo").Skip == mcp.SkipChanged })
	if got := toolNamesOf(r.src.Tools()); !slices.Equal(got, all[1:]) {
		t.Fatalf("a changed tool offered: %v", got)
	}
	r.f.Fail(http.StatusServiceUnavailable)
	r.cat.Refresh("srv")
	eventually(t, "failing", func() bool { st, _ := r.cat.State("srv"); return !st.FailingSince.IsZero() })
	r.clock.Advance(mcp.KeepFor)
	if got := r.src.Tools(); len(got) != 0 {
		t.Fatalf("a withdrawn server's tools offered: %v", toolNamesOf(got))
	}
}
