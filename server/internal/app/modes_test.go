package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

type fakeModes map[string]agent.Mode

func (f fakeModes) Get(_ context.Context, name string) (agent.Mode, error) {
	if m, ok := f[name]; ok {
		return m, nil
	}
	return agent.Mode{}, agent.ErrModeNotFound
}

func TestDescribedModesSkipsMissingRow(t *testing.T) {
	got, err := describedModes(context.Background(), fakeModes{"fast": {Name: "fast"}}, []string{"fast", "smart"}, slog.New(slog.DiscardHandler))
	if err != nil || len(got) != 1 || got[0].Name != "fast" {
		t.Fatalf("got %+v %v", got, err)
	}
}

// liveModes are modes and a global allowed list a test changes
type liveModes struct {
	mu      sync.Mutex
	modes   fakeModes
	allowed []string
	fail    bool
	loads   int
	at      []time.Time // when each load began
}

func (l *liveModes) Get(ctx context.Context, name string) (agent.Mode, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail {
		return agent.Mode{}, errors.New("down")
	}
	return l.modes.Get(ctx, name)
}

func (l *liveModes) Allowed(context.Context) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loads++
	l.at = append(l.at, time.Now())
	return l.allowed, nil
}

func (l *liveModes) change(fn func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn()
}

func (l *liveModes) loaded() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loads
}

// agent_mode's description follows a mode put and the global allowed list:
// each poke loads them again, and a failed load keeps what it had
func TestDescribedModesFollowChanges(t *testing.T) {
	live := &liveModes{
		modes: fakeModes{
			"fast":  {Name: "fast", Aliases: map[string][]string{"en": {"quick"}}},
			"smart": {Name: "smart"},
		},
		allowed: settings.Defaults().AllowedModes,
	}
	list, poke, stop := watchModes(context.Background(), live, live.Allowed, slog.New(slog.DiscardHandler))
	defer stop()
	reg := agent.NewRegistry(tools.AgentMode(tools.ModeDeps{
		Settings: prefsOf(settings.Values{}), State: agent.NewModeState(), Modes: live, Described: list.Modes,
	}))
	desc := func() string { return reg.Offer(nil, false, cadencev2.Trigger_TRIGGER_WAKE)[0].Description }
	if d := desc(); !strings.Contains(d, "fast (also: quick); smart;") {
		t.Fatalf("at start: %s", d)
	}
	live.change(func() { live.modes["fast"] = agent.Mode{Name: "fast", Aliases: map[string][]string{"en": {"rapid"}}} })
	poke()
	waitFor(t, "the put alias", func() bool { return strings.Contains(desc(), "fast (also: rapid); smart;") })
	live.change(func() {
		live.modes["turbo"] = agent.Mode{Name: "turbo"}
		live.allowed = []string{"fast", "turbo"}
	})
	poke()
	waitFor(t, "the allowed list", func() bool { return strings.Contains(desc(), "fast (also: rapid); turbo;") })
	before := live.loaded()
	live.change(func() { live.fail = true })
	poke()
	waitFor(t, "a failed load", func() bool { return live.loaded() > before })
	stop()
	if d := desc(); !strings.Contains(d, "fast (also: rapid); turbo;") {
		t.Fatalf("a failed load changed it: %s", d)
	}
}

func prefsOf(v settings.Values) tools.SettingsFunc {
	return func(context.Context, string) (settings.Values, error) { return v, nil }
}

// a failed load is tried again on its own, sooner at first, so a store down at
// startup leaves agent_mode's description empty only until it answers, not
// until the next mode or settings write (no poke here)
func TestDescribedModesRetryAFailedLoad(t *testing.T) {
	live := &liveModes{modes: fakeModes{"fast": {Name: "fast"}, "smart": {Name: "smart"}}, allowed: settings.Defaults().AllowedModes, fail: true}
	list, _, stop := watchModesRetrying(context.Background(), live, live.Allowed, slog.New(slog.DiscardHandler), retry{base: 5 * time.Millisecond, limit: 20 * time.Millisecond})
	defer stop()
	if got := list.Modes(); len(got) != 0 {
		t.Fatalf("at start: %+v", got)
	}
	waitFor(t, "tries again", func() bool { return live.loaded() >= 3 })
	live.change(func() { live.fail = false })
	waitFor(t, "the list once the store answers", func() bool { return len(list.Modes()) == 2 })
	// a load that succeeded is not tried again
	n := live.loaded()
	time.Sleep(60 * time.Millisecond)
	if live.loaded() != n {
		t.Fatalf("loaded %d times after a success, then %d", n, live.loaded())
	}
}

// the retries back off from the first one: base, then twice that, …
func TestDescribedModesRetryBacksOff(t *testing.T) {
	const base = 40 * time.Millisecond
	live := &liveModes{modes: fakeModes{"fast": {Name: "fast"}}, allowed: []string{"fast"}, fail: true}
	_, _, stop := watchModesRetrying(context.Background(), live, live.Allowed, slog.New(slog.DiscardHandler), retry{base: base, limit: time.Second})
	defer stop()
	waitFor(t, "three retries", func() bool { return live.loaded() >= 4 })
	stop()
	live.mu.Lock()
	defer live.mu.Unlock()
	for i, want := range []time.Duration{base, 2 * base, 4 * base} {
		if gap := live.at[i+1].Sub(live.at[i]); gap < want {
			t.Fatalf("retry %d after %v, want at least %v", i+1, gap, want)
		}
	}
}
