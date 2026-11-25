package agent_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func revert(policy string) settings.Values {
	v := settings.Defaults()
	v.ModeRevert = policy
	return v
}

type modeCheck struct {
	t *testing.T
	s *agent.ModeState
	v settings.Values
}

func (c modeCheck) want(device string, at time.Duration, mode string) {
	c.t.Helper()
	if got := c.s.Current(device, c.v, t0.Add(at)); got != mode {
		c.t.Fatalf("%s at +%v: got %q, want %q", device, at, got, mode)
	}
}

func TestModeRevertAfterIdle(t *testing.T) {
	c := modeCheck{t, agent.NewModeState(), revert("after_idle")}
	c.want("d1", 0, "fast")
	c.s.Set("d1", "s1", "smart", t0)
	c.want("d1", 9*time.Minute, "smart")
	c.want("d1", 18*time.Minute, "smart") // idle counts from the latest turn, not from the switch
	c.want("d2", 18*time.Minute, "fast")
	c.s.EndSession("d1", "s1")
	c.want("d1", 27*time.Minute, "smart")
	c.want("d1", 37*time.Minute, "fast")
	c.want("d1", 38*time.Minute, "fast")

	c.v = revert("never")
	c.s.Set("d1", "s2", "smart", t0.Add(40*time.Minute))
	c.s.EndSession("d1", "s2")
	c.want("d1", 24*time.Hour, "smart")
}

func TestModeRevertAfterSession(t *testing.T) {
	c := modeCheck{t, agent.NewModeState(), revert("after_session")}
	c.s.Set("d1", "s1", "smart", t0)
	c.want("d1", time.Minute, "smart")
	c.want("d1", time.Hour, "smart") // idle alone does not end a session
	c.s.EndSession("d1", "s0")       // an older session's late end
	c.s.EndSession("d2", "s1")
	c.want("d1", time.Hour, "smart")
	c.s.EndSession("d1", "s1")
	c.want("d1", time.Hour, "fast")
	c.s.Set("d1", "s2", "smart", t0.Add(time.Hour))
	c.want("d1", time.Hour, "smart")

	c.s.EndSession("d1", "s3")
	c.s.Set("d1", "s3", "smart", t0.Add(2*time.Hour)) // a late set for a session that already ended
	c.want("d1", 2*time.Hour, "fast")

	c.v = revert("after_idle")
	c.s.EndSession("d1", "s4")
	c.s.Set("d1", "s4", "smart", t0.Add(3*time.Hour))
	c.want("d1", 3*time.Hour, "smart")
}

func TestModePeekReadsOnly(t *testing.T) {
	s := agent.NewModeState()
	v := revert("after_idle")
	if got := s.Peek("d1", v, t0); got != "fast" {
		t.Fatalf("nothing set: got %q", got)
	}
	s.Set("d1", "s1", "smart", t0)
	if got := s.Peek("d1", v, t0.Add(9*time.Minute)); got != "smart" {
		t.Fatalf("got %q", got)
	}
	if got := s.Peek("d1", v, t0.Add(10*time.Minute)); got != "fast" {
		t.Fatalf("a peek extended the idle window: got %q", got)
	}
	narrowed := v
	narrowed.AllowedModes = []string{"fast"}
	if got := s.Peek("d1", narrowed, t0.Add(time.Minute)); got != "fast" {
		t.Fatalf("a mode no longer allowed: got %q", got)
	}
	if got := s.Current("d1", v, t0.Add(time.Minute)); got != "smart" {
		t.Fatalf("a peek changed the state: got %q", got)
	}
}

func TestModeCurrentFallsBack(t *testing.T) {
	s := agent.NewModeState()
	cases := []struct {
		mode    string
		allowed []string
		want    string
	}{
		{"smart", []string{"fast", "smart"}, "smart"},
		{"smart", []string{"deep", "fast"}, "deep"},
		{"smart", nil, "fast"},
	}
	for _, c := range cases {
		v := settings.Defaults()
		v.Mode, v.AllowedModes = c.mode, c.allowed
		if got := s.Current("d1", v, t0); got != c.want {
			t.Errorf("mode %q allowed %v: got %q, want %q", c.mode, c.allowed, got, c.want)
		}
	}
	s.Set("d1", "s1", "smart", t0)
	v := settings.Defaults()
	v.AllowedModes = []string{"fast"}
	if got := s.Current("d1", v, t0); got != "fast" {
		t.Fatalf("a voice mode no longer allowed: got %q", got)
	}
	v.AllowedModes = []string{"fast", "smart"}
	if got := s.Current("d1", v, t0); got != "fast" {
		t.Fatalf("a dropped voice mode came back: got %q", got)
	}
}

func TestModeStateConcurrent(t *testing.T) {
	s := agent.NewModeState()
	v := settings.Defaults()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dev := fmt.Sprintf("d%d", i%3)
			for j := range 200 {
				at := t0.Add(time.Duration(j) * time.Second)
				sess := fmt.Sprintf("s%d", j%4)
				s.Set(dev, sess, "smart", at)
				if got := s.Current(dev, v, at); got != "smart" && got != "fast" {
					t.Errorf("got %q", got)
				}
				if got := s.Peek(dev, v, at); got != "smart" && got != "fast" {
					t.Errorf("peek got %q", got)
				}
				s.EndSession(dev, sess)
			}
		}()
	}
	wg.Wait()
}
