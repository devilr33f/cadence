package sim_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// outputIn is the sim's audio.output, its level kept under dir ("" keeps it in memory).
func outputIn(t *testing.T, dir string) (board.Board, capability.Provider) {
	t.Helper()
	b := sim.New(func(string) string { return "1" }, dir, nil)
	caps := b.Capabilities()
	if len(caps) != 1 || caps[0].Descriptor().ID != "audio.output" {
		t.Fatalf("caps = %v", caps)
	}
	return b, caps[0]
}

func output(t *testing.T) capability.Provider {
	t.Helper()
	_, p := outputIn(t, "")
	return p
}

func next(t *testing.T, p capability.Provider) string {
	t.Helper()
	select {
	case s := <-p.State():
		return string(s)
	case <-time.After(time.Second):
		t.Fatal("no state")
		return ""
	}
}

func noState(t *testing.T, p capability.Provider) {
	t.Helper()
	select {
	case s := <-p.State():
		t.Fatalf("state: %s", s)
	default:
	}
}

func storeLevel(t *testing.T, dir, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "volume"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, p capability.Provider, name, args string) (string, error) {
	t.Helper()
	res, err := p.Command(context.Background(), name, json.RawMessage(args))
	return string(res), err
}

func TestDescriptor(t *testing.T) {
	d := output(t).Descriptor()
	if d.ConfigSchema != `{"type":"object","properties":{},"additionalProperties":false}` {
		t.Fatalf("config schema %s", d.ConfigSchema)
	}
	if len(d.Commands) != 2 || d.Commands["set_level"] == "" || d.Commands["change_level"] == "" {
		t.Fatalf("commands %v", d.Commands)
	}
}

func TestInitialState(t *testing.T) {
	if got := next(t, output(t)); got != `{"level":50,"channels":1,"maxBitrate":64000}` {
		t.Fatalf("state = %s", got)
	}
}

func TestStartsAtTheLocalLevel(t *testing.T) {
	dir := t.TempDir()
	storeLevel(t, dir, `{"level":30}`)
	_, p := outputIn(t, dir)
	if got := next(t, p); got != `{"level":30,"channels":1,"maxBitrate":64000}` {
		t.Fatalf("state = %s", got)
	}
}

func TestSetLevelPersists(t *testing.T) {
	dir := t.TempDir()
	_, p := outputIn(t, dir)
	next(t, p)
	if got, err := run(t, p, "set_level", `{"level":30}`); err != nil || got != `{"level":30}` {
		t.Fatalf("set_level = %s, %v", got, err)
	}
	if got := next(t, p); got != `{"level":30,"channels":1,"maxBitrate":64000}` {
		t.Fatalf("state = %s", got)
	}
	if got, err := run(t, p, "change_level", `{"delta":-10}`); err != nil || got != `{"level":20}` {
		t.Fatalf("change_level = %s, %v", got, err)
	}
	_, again := outputIn(t, dir)
	if got := next(t, again); got != `{"level":20,"channels":1,"maxBitrate":64000}` {
		t.Fatalf("after a restart = %s", got)
	}
}

func TestStateKeepsLatestWhenUnread(t *testing.T) {
	p := output(t)
	for _, l := range []string{`{"level":10}`, `{"level":20}`, `{"level":30}`} {
		if _, err := run(t, p, "set_level", l); err != nil {
			t.Fatal(err)
		}
	}
	if got := next(t, p); got != `{"level":30,"channels":1,"maxBitrate":64000}` {
		t.Fatalf("state = %s", got)
	}
}

func TestApplyTakesNoLevel(t *testing.T) {
	p := output(t)
	next(t, p)
	if err := p.Apply(context.Background(), 1, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	noState(t, p)
	for _, bad := range []string{`{"level":101}`, `{"level":-1}`, `{"level":1.5}`, `{"level":5,"x":1}`, `{"x":1}`, `nope`, `[]`} {
		err := p.Apply(context.Background(), 1, json.RawMessage(bad))
		var ae *capability.ApplyError
		if !errors.As(err, &ae) || ae.Code != "invalid" {
			t.Fatalf("%s: err = %v", bad, err)
		}
	}
	noState(t, p)
}

// a level config stored before the device owned its volume seeds the level once
func TestLegacyConfigSeedsAMissingLevel(t *testing.T) {
	dir := t.TempDir()
	_, p := outputIn(t, dir)
	next(t, p)
	if err := p.Apply(context.Background(), 4, json.RawMessage(`{"level":30}`)); err != nil {
		t.Fatal(err)
	}
	if got := next(t, p); got != `{"level":30,"channels":1,"maxBitrate":64000}` {
		t.Fatalf("state = %s", got)
	}
	if err := p.Apply(context.Background(), 5, json.RawMessage(`{"level":70}`)); err != nil {
		t.Fatal(err)
	}
	noState(t, p)
	_, again := outputIn(t, dir)
	if got := next(t, again); got != `{"level":30,"channels":1,"maxBitrate":64000}` {
		t.Fatalf("after a restart = %s", got)
	}
}

func TestLegacyConfigNeverOverridesTheLocalLevel(t *testing.T) {
	dir := t.TempDir()
	storeLevel(t, dir, `{"level":20}`)
	_, p := outputIn(t, dir)
	next(t, p)
	if err := p.Apply(context.Background(), 4, json.RawMessage(`{"level":70}`)); err != nil {
		t.Fatal(err)
	}
	noState(t, p)
	if got, err := run(t, p, "change_level", `{"delta":0}`); err != nil || got != `{"level":20}` {
		t.Fatalf("level = %s, %v", got, err)
	}
}

// rule 7: the sim's own sessions decide a raise
func TestRaiseNeedsTheSessionTheUserWoke(t *testing.T) {
	b, p := outputIn(t, "")
	recent := b.(board.SessionTracker).Sessions()
	recent.Record("follow", cadencev2.Trigger_TRIGGER_FOLLOW_UP)
	recent.Record("wake", cadencev2.Trigger_TRIGGER_WAKE)
	var ce *capability.CommandError
	if _, err := run(t, p, "set_level", `{"level":60,"sessionId":"follow"}`); !errors.As(err, &ce) || ce.Code != "wake_required" {
		t.Fatalf("follow-up raise: %v", err)
	}
	if got, err := run(t, p, "set_level", `{"level":60,"sessionId":"wake"}`); err != nil || got != `{"level":60}` {
		t.Fatalf("woken raise = %s, %v", got, err)
	}
}
