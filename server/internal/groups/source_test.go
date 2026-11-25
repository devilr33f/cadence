package groups

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestPickSource(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	speakers := map[string]Facts{devA: up("sim", ""), devB: up("sim", ""), devC: up("sim-tv", "")}
	on := func(raised int) sourceSeen { return sourceSeen{local: true, raised: at(raised)} }
	off := func(dropped int) sourceSeen { return sourceSeen{raised: at(0), dropped: at(dropped)} }
	cases := []struct {
		name   string
		facts  map[string]Facts
		seen   map[string]sourceSeen
		last   lead
		now    int
		want   choice
		wantAt int // the wake, seconds; -1 none
	}{
		{name: "nobody plays: the clock master", facts: speakers, now: 100,
			want: choice{source: devA, reason: reasonPreferred}, wantAt: -1},
		{name: "a source beats the preferred one", facts: speakers, seen: map[string]sourceSeen{devC: on(90)}, now: 100,
			want: choice{source: devC, reason: reasonSource}, wantAt: -1},
		{name: "the latest to raise it wins", facts: speakers, seen: map[string]sourceSeen{devB: on(95), devC: on(90)},
			last: lead{source: devC, movedAt: at(50)}, now: 100, want: choice{source: devB, reason: reasonSource}, wantAt: -1},
		{name: "the clock master's own source keeps it leading", facts: speakers, seen: map[string]sourceSeen{devA: on(95)},
			now: 100, want: choice{source: devA, reason: reasonSource}, wantAt: -1},
		{name: "an idle source keeps the lead through the grace", facts: speakers, seen: map[string]sourceSeen{devC: off(80)},
			last: lead{source: devC, movedAt: at(10)}, now: 100, want: choice{source: devC, reason: reasonGrace}, wantAt: 110},
		{name: "after the grace, back to the clock master", facts: speakers, seen: map[string]sourceSeen{devC: off(70)},
			last: lead{source: devC, movedAt: at(10)}, now: 100, want: choice{source: devA, reason: reasonPreferred}, wantAt: -1},
		{name: "an idle source whose drop the server never saw does not keep it", facts: speakers,
			last: lead{source: devC, movedAt: at(10)}, now: 100, want: choice{source: devA, reason: reasonPreferred}, wantAt: -1},
		{name: "another source within the grace takes it", facts: speakers,
			seen: map[string]sourceSeen{devC: off(95), devB: on(98)}, last: lead{source: devC, movedAt: at(10)}, now: 100,
			want: choice{source: devB, reason: reasonSource}, wantAt: -1},
		{name: "a source going offline: the clock master at once", facts: map[string]Facts{devA: up("sim", ""), devB: up("sim", ""),
			devC: down("sim-tv", "")}, seen: map[string]sourceSeen{devC: on(90)}, last: lead{source: devC, movedAt: at(95)}, now: 100,
			want: choice{source: devA, reason: reasonPreferred}, wantAt: -1},
		{name: "a source with no address cannot lead", facts: map[string]Facts{devA: up("sim", ""), devB: up("sim", ""),
			devC: {Model: "sim-tv", Online: true, Capable: true}}, seen: map[string]sourceSeen{devC: on(90)}, now: 100,
			want: choice{source: devA, reason: reasonPreferred}, wantAt: -1},
		{name: "flapping: a move within 10 s of the last is held", facts: speakers,
			seen: map[string]sourceSeen{devB: on(99), devC: on(90)}, last: lead{source: devC, movedAt: at(95)}, now: 100,
			want: choice{source: devC, reason: reasonFlapping, want: devB}, wantAt: 105},
		{name: "flapping: the hold ends after 10 s", facts: speakers,
			seen: map[string]sourceSeen{devB: on(99), devC: on(90)}, last: lead{source: devC, movedAt: at(90)}, now: 100,
			want: choice{source: devB, reason: reasonSource}, wantAt: -1},
		{name: "the clock master back within 10 s of a move takes over at once", facts: speakers,
			last: lead{source: devB, movedAt: at(95)}, now: 100, want: choice{source: devA, reason: reasonPreferred}, wantAt: -1},
		{name: "nobody can lead", facts: map[string]Facts{devA: down("sim", ""), devB: down("sim", ""), devC: down("sim-tv", "")},
			seen: map[string]sourceSeen{devC: on(90)}, last: lead{source: devC}, now: 100, want: choice{}, wantAt: -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := Group{ID: gid, Kind: KindGroup, Members: members(devA, devB, devC)}
			got := pickSource(g, c.facts, elect(g, c.facts), c.seen, c.last, at(c.now))
			want := c.want
			if c.wantAt >= 0 {
				want.wake = at(c.wantAt)
			}
			if got != want {
				t.Fatalf("got %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestSourceSeen(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	var s sourceSeen
	s = s.see(true, t0)
	s = s.see(true, t0.Add(time.Second))
	if !s.local || !s.raised.Equal(t0) || !s.dropped.IsZero() {
		t.Fatalf("raised %+v", s)
	}
	s = s.see(false, t0.Add(2*time.Second))
	s = s.see(false, t0.Add(3*time.Second))
	if s.local || !s.dropped.Equal(t0.Add(2*time.Second)) || !s.raised.Equal(t0) {
		t.Fatalf("dropped %+v", s)
	}
}

// the leads move over time as the members raise and drop their sources, each move logged with ids alone
func TestElectSourcesOverTime(t *testing.T) {
	var buf bytes.Buffer
	s := New(Deps{Log: slog.New(slog.NewTextHandler(&buf, nil))})
	defer func() {
		if s.wake != nil {
			s.wake.Stop()
		}
	}()
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	facts := map[string]Facts{devA: up("sim", ""), devB: up("sim", ""), devC: up("sim-tv", "")}
	groups := []keyed{{Group: Group{ID: gid, Kind: KindGroup, Members: members(devA, devB, devC)}}}
	step := func(sec int, local map[string]bool, want string) time.Time {
		t.Helper()
		now := t0.Add(time.Duration(sec) * time.Second)
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, id := range []string{devA, devB, devC} {
			s.seen[id] = s.seen[id].see(local[id], now)
		}
		got, wake := s.electSourcesLocked(groups, facts, now)
		if got[gid] != want {
			t.Fatalf("at %ds: source %q, want %q", sec, got[gid], want)
		}
		return wake
	}
	step(0, nil, devA)
	step(5, map[string]bool{devC: true}, devC)  // the tv plays a film
	step(20, map[string]bool{devB: true}, devB) // a cast on B: the latest wins
	step(22, map[string]bool{devC: true}, devB) // the tv again within 10 s: held
	step(25, map[string]bool{devC: true}, devB) // still held, logged once
	step(31, map[string]bool{devC: true}, devC) // the hold over
	wake := step(40, nil, devC)                 // the tv paused: grace
	if want := t0.Add(40*time.Second + sourceGrace); !wake.Equal(want) {
		t.Fatalf("wake %v, want %v", wake, want)
	}
	step(69, nil, devC)
	step(71, nil, devA) // the grace over: the clock master
	log := buf.String()
	for _, want := range []string{
		"group_leader_moved group_id=" + gid + " from=" + devA + " to=" + devC + " reason=source",
		"group_leader_moved group_id=" + gid + " from=" + devC + " to=" + devB + " reason=source",
		"group_leader_held group_id=" + gid + " from=" + devB + " to=" + devC + " reason=flapping",
		"group_leader_moved group_id=" + gid + " from=" + devB + " to=" + devC + " reason=source",
		"group_leader_moved group_id=" + gid + " from=" + devC + " to=" + devA + " reason=preferred",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("no %q in\n%s", want, log)
		}
	}
	if n := strings.Count(log, "group_leader_held"); n != 1 {
		t.Errorf("held logged %d times:\n%s", n, log)
	}
	if n := strings.Count(log, "group_leader_moved"); n != 4 {
		t.Errorf("%d moves:\n%s", n, log)
	}
}
