package memory_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/memory"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// embedFake answers every input with vec; it can stall or fail.
type embedFake struct {
	model string
	vec   []float32
	delay time.Duration
	err   error

	mu     sync.Mutex
	inputs [][]string
}

func (e *embedFake) Model() string { return e.model }

func (e *embedFake) Embed(ctx context.Context, in []string) ([][]float32, error) {
	e.mu.Lock()
	e.inputs = append(e.inputs, in)
	e.mu.Unlock()
	if e.delay > 0 {
		select {
		case <-time.After(e.delay):
		case <-ctx.Done():
			return nil, errors.New(providers.CodeEmbedTimeout)
		}
	}
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(in))
	for i := range in {
		out[i] = e.vec
	}
	return out, nil
}

func (e *embedFake) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.inputs)
}

type embeds struct {
	byName map[string]providers.Embedder
	err    error
}

func (e embeds) Embed(_ context.Context, name string) (providers.Embedder, error) {
	if e.err != nil {
		return nil, e.err
	}
	if v, ok := e.byName[name]; ok {
		return v, nil
	}
	return nil, providers.ErrNotFound
}

type recallRig struct {
	*rig
	emb  *embedFake
	logs *syncBuffer // late hit goroutines log into it too
	now  time.Time
	rc   *memory.Recaller
}

func newRecallRig(t *testing.T) *recallRig {
	r := &recallRig{rig: newRig(t), emb: &embedFake{model: model, vec: []float32{1, 0, 0}}, logs: &syncBuffer{}, now: time.Now()}
	r.rc = memory.NewRecaller(memory.RecallerDeps{
		Store: r.s, Embeds: embeds{byName: map[string]providers.Embedder{"emb": r.emb}},
		Log: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return r.now },
	})
	return r
}

func (r *recallRig) recall(input string, scopes []string) memory.Recalled {
	return r.rc.Recall(context.Background(), memory.Query{DeviceID: r.d1, Input: input, Embed: "emb", Scopes: scopes})
}

// near is a vector at a growing angle from the query (1,0,0).
func near(k float32) []float32 { return []float32{1, k, 0} }

func recalledTexts(res memory.Recalled) []string { return texts(res.Memories) }

func TestRecallPinnedThenNearestDeduplicated(t *testing.T) {
	r := newRecallRig(t)
	r.add(memory.NewMemory{Text: "pinned-far", Pinned: true, Vector: []float32{0, 1, 0}, EmbedModel: model})
	r.add(memory.NewMemory{Text: "pinned-near", Pinned: true, Vector: near(0.05), EmbedModel: model})
	r.add(memory.NewMemory{Text: "pinned-unembedded", Pinned: true})
	for i := 1; i <= 10; i++ {
		r.add(memory.NewMemory{Text: fmt.Sprintf("n%02d", i), Vector: near(float32(i) * 0.1), EmbedModel: model})
	}
	r.add(memory.NewMemory{Text: "far", Vector: []float32{0, 1, 0}, EmbedModel: model})
	res := r.recall("hello", nil)
	want := []string{"pinned-far", "pinned-near", "pinned-unembedded", "n01", "n02", "n03", "n04", "n05", "n06", "n07", "n08"}
	// pinned first in creation order, then the 8 nearest others by similarity; the pinned near one only once
	if got := recalledTexts(res); !eq(got, want) {
		t.Fatalf("got %v", got)
	}
	if res.Pinned != 3 || res.Skipped != "" {
		t.Fatalf("%+v", res)
	}
}

func TestRecallMinSimilarity(t *testing.T) {
	r := newRecallRig(t)
	r.add(memory.NewMemory{Text: "close", Vector: near(1), EmbedModel: model})       // 0.707
	r.add(memory.NewMemory{Text: "edge-out", Vector: near(3.5), EmbedModel: model})  // 0.275
	r.add(memory.NewMemory{Text: "edge-in", Vector: near(3), EmbedModel: model})     // 0.316
	r.add(memory.NewMemory{Text: "other-model", Vector: near(0), EmbedModel: "zzz"}) // not this model
	if got := recalledTexts(r.recall("hello", nil)); !eq(got, []string{"close", "edge-in"}) {
		t.Fatalf("got %v", got)
	}
}

func TestRecallScopes(t *testing.T) {
	r := newRecallRig(t)
	r.add(memory.NewMemory{Text: "g", Vector: near(0.1), EmbedModel: model})
	r.add(memory.NewMemory{Text: "g-pin", Pinned: true})
	r.add(memory.NewMemory{Text: "mine", Scope: memory.ScopeDevice, DeviceID: r.d1, Vector: near(0.2), EmbedModel: model})
	r.add(memory.NewMemory{Text: "mine-pin", Scope: memory.ScopeDevice, DeviceID: r.d1, Pinned: true})
	r.add(memory.NewMemory{Text: "theirs", Scope: memory.ScopeDevice, DeviceID: r.d2, Vector: near(0.01), EmbedModel: model})
	r.add(memory.NewMemory{Text: "theirs-pin", Scope: memory.ScopeDevice, DeviceID: r.d2, Pinned: true})
	r.add(memory.NewMemory{Text: "pending", Pinned: true, Status: memory.StatusPending})
	cases := []struct {
		name   string
		scopes []string
		want   []string
	}{
		{"unset is both", nil, []string{"g-pin", "mine-pin", "g", "mine"}},
		{"both", []string{"global", "device"}, []string{"g-pin", "mine-pin", "g", "mine"}},
		{"global excluded", []string{"device"}, []string{"mine-pin", "mine"}},
		{"device excluded", []string{"global"}, []string{"g-pin", "g"}},
		{"none", []string{}, nil},
	}
	for _, c := range cases {
		res := r.recall("hello", c.scopes)
		if got := recalledTexts(res); !eq(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	if r.emb.calls() != 4 {
		t.Fatalf("embedder asked %d times; no scope needs none", r.emb.calls())
	}
}

func TestRecallTrimsToBudgetPinnedFirst(t *testing.T) {
	r := newRecallRig(t)
	long := func(c string, n int) string { return strings.Repeat(c, n) }
	r.add(memory.NewMemory{Text: long("a", 500), Pinned: true})
	r.add(memory.NewMemory{Text: long("b", 500), Pinned: true})
	r.add(memory.NewMemory{Text: long("c", 400), Vector: near(0.1), EmbedModel: model})
	r.add(memory.NewMemory{Text: long("d", 150), Vector: near(0.2), EmbedModel: model})
	r.add(memory.NewMemory{Text: long("e", 50), Vector: near(0.3), EmbedModel: model})
	res := r.recall("hello", nil)
	// 500 + 500 + 400 = 1400; d (150) does not fit, e (50) does
	got := recalledTexts(res)
	if len(got) != 4 || got[0][0] != 'a' || got[1][0] != 'b' || got[2][0] != 'c' || got[3][0] != 'e' {
		t.Fatalf("got %d texts", len(got))
	}
	if res.Pinned != 2 {
		t.Fatalf("pinned %d", res.Pinned)
	}
}

func TestRecallCountsHits(t *testing.T) {
	r := newRecallRig(t)
	a := r.add(memory.NewMemory{Text: "pin", Pinned: true})
	b := r.add(memory.NewMemory{Text: "near", Vector: near(0.1), EmbedModel: model})
	c := r.add(memory.NewMemory{Text: "far", Vector: []float32{0, 1, 0}, EmbedModel: model})
	r.recall("hello", nil)
	ctx := context.Background()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ga, _ := r.s.Get(ctx, a.ID)
		gb, _ := r.s.Get(ctx, b.ID)
		if ga.Hits == 1 && gb.Hits == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hits %d %d", ga.Hits, gb.Hits)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if gc, _ := r.s.Get(ctx, c.ID); gc.Hits != 0 {
		t.Fatalf("not recalled but hit: %d", gc.Hits)
	}
}

func TestRecallEmbedBudget(t *testing.T) {
	r := newRecallRig(t)
	r.add(memory.NewMemory{Text: "pin", Pinned: true})
	r.add(memory.NewMemory{Text: "near", Vector: near(0.1), EmbedModel: model})
	r.emb.delay = 3 * time.Second
	start := time.Now()
	res := r.recall("hello", nil)
	if d := time.Since(start); d > 450*time.Millisecond {
		t.Fatalf("recall took %v", d)
	}
	if got := recalledTexts(res); !eq(got, []string{"pin"}) || res.Skipped != "timeout" {
		t.Fatalf("%v %+v", got, res)
	}
	if !strings.Contains(r.logs.String(), "memory_embed_skipped") || !strings.Contains(r.logs.String(), "reason=timeout") {
		t.Fatalf("log %s", r.logs.String())
	}
}

// an embedder that ignores its context still cannot stall the turn
func TestRecallEmbedBudgetWithoutContext(t *testing.T) {
	r := newRecallRig(t)
	r.add(memory.NewMemory{Text: "pin", Pinned: true})
	r.rc = memory.NewRecaller(memory.RecallerDeps{Store: r.s, Embeds: embeds{byName: map[string]providers.Embedder{"emb": deaf{}}}})
	start := time.Now()
	res := r.recall("hello", nil)
	if d := time.Since(start); d > 450*time.Millisecond {
		t.Fatalf("recall took %v", d)
	}
	if got := recalledTexts(res); !eq(got, []string{"pin"}) || res.Skipped != "timeout" {
		t.Fatalf("%v %+v", got, res)
	}
}

type deaf struct{}

func (deaf) Model() string { return model }
func (deaf) Embed(context.Context, []string) ([][]float32, error) {
	time.Sleep(2 * time.Second)
	return [][]float32{{1, 0, 0}}, nil
}

func TestRecallFallsBackToPinned(t *testing.T) {
	r := newRecallRig(t)
	r.add(memory.NewMemory{Text: "pin", Pinned: true})
	r.add(memory.NewMemory{Text: "near", Vector: near(0.1), EmbedModel: model})
	cases := []struct {
		name   string
		setup  func()
		embed  string
		input  string
		reason string
		calls  int
	}{
		{"embedder error", func() { r.emb.err = errors.New(providers.CodeEmbedFailed) }, "emb", "hello", "error", 1},
		{"dimension", func() { r.emb.err, r.emb.vec = nil, []float32{1, 0} }, "emb", "hello", "dimension", 1},
		{"empty input", func() { r.emb.vec = []float32{1, 0, 0} }, "emb", "  \n", "no_input", 0},
		{"no instance", func() {}, "", "hello", "no_instance", 0},
		{"unknown instance", func() {}, "ghost", "hello", "unknown_instance", 0},
	}
	for _, c := range cases {
		c.setup()
		before := r.emb.calls()
		res := r.rc.Recall(context.Background(), memory.Query{DeviceID: r.d1, Input: c.input, Embed: c.embed})
		if got := recalledTexts(res); !eq(got, []string{"pin"}) || res.Skipped != c.reason || res.Pinned != 1 {
			t.Errorf("%s: %v %+v", c.name, got, res)
		}
		if n := r.emb.calls() - before; n != c.calls {
			t.Errorf("%s: %d embedder calls, want %d", c.name, n, c.calls)
		}
	}
	// the memory text is never logged
	if strings.Contains(r.logs.String(), "near") || strings.Contains(r.logs.String(), "hello") {
		t.Fatalf("log %s", r.logs.String())
	}
}

func TestRecallUnknownInstanceLoggedOncePerNamePerHour(t *testing.T) {
	r := newRecallRig(t)
	ask := func(name string) {
		r.rc.Recall(context.Background(), memory.Query{DeviceID: r.d1, Input: "hello", Embed: name})
	}
	count := func() int { return strings.Count(r.logs.String(), "memory_embed_skipped") }
	ask("ghost")
	ask("ghost")
	if count() != 1 {
		t.Fatalf("log %s", r.logs.String())
	}
	ask("phantom")
	if count() != 2 {
		t.Fatalf("log %s", r.logs.String())
	}
	r.now = r.now.Add(59 * time.Minute)
	ask("ghost")
	if count() != 2 {
		t.Fatalf("log %s", r.logs.String())
	}
	r.now = r.now.Add(2 * time.Minute)
	ask("ghost")
	if count() != 3 {
		t.Fatalf("log %s", r.logs.String())
	}
}

// the caller's own deadline cutting in keeps the pinned memories
func TestRecallCallerDeadlineKeepsPinned(t *testing.T) {
	r := newRecallRig(t)
	r.add(memory.NewMemory{Text: "pin", Pinned: true})
	r.add(memory.NewMemory{Text: "near", Vector: near(0.1), EmbedModel: model})
	r.emb.delay = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := r.rc.Recall(ctx, memory.Query{DeviceID: r.d1, Input: "hello", Embed: "emb"})
	if d := time.Since(start); d > 450*time.Millisecond {
		t.Fatalf("recall took %v", d)
	}
	if got := recalledTexts(res); !eq(got, []string{"pin"}) || res.Skipped != "timeout" {
		t.Fatalf("%v %+v", got, res)
	}
}

func globalSetting(name string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return name, nil }
}

func TestRecallModelMismatchWithGlobalEmbed(t *testing.T) {
	r := newRecallRig(t)
	r.add(memory.NewMemory{Text: "pin", Pinned: true})
	r.add(memory.NewMemory{Text: "near", Vector: near(0.1), EmbedModel: model})
	other := &embedFake{model: "embed-other", vec: []float32{1, 0, 0}}
	embs := embeds{byName: map[string]providers.Embedder{"emb": r.emb, "dev": other, "same": &embedFake{model: model, vec: []float32{1, 0, 0}}}}
	mk := func(global string) *memory.Recaller {
		return memory.NewRecaller(memory.RecallerDeps{
			Store: r.s, Embeds: embs, GlobalEmbed: globalSetting(global),
			Log: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return r.now },
		})
	}
	ask := func(rc *memory.Recaller, name string) memory.Recalled {
		return rc.Recall(context.Background(), memory.Query{DeviceID: r.d1, Input: "hello", Embed: name})
	}
	// the device's model differs from the household's: its rows never match, say so
	res := ask(mk("emb"), "dev")
	if got := recalledTexts(res); !eq(got, []string{"pin"}) || res.Skipped != "model_mismatch" {
		t.Fatalf("%v %+v", got, res)
	}
	if !strings.Contains(r.logs.String(), "reason=model_mismatch") {
		t.Fatalf("log %s", r.logs.String())
	}
	// another instance of the same model is no mismatch
	if res := ask(mk("emb"), "same"); res.Skipped != "" || len(res.Memories) != 2 {
		t.Fatalf("%+v", res)
	}
	if res := ask(mk("emb"), "emb"); res.Skipped != "" || len(res.Memories) != 2 {
		t.Fatalf("%+v", res)
	}
}

func TestRecallLogsGlobalEmbedUnsetOncePerHour(t *testing.T) {
	r := newRecallRig(t)
	rc := memory.NewRecaller(memory.RecallerDeps{
		Store: r.s, Embeds: embeds{byName: map[string]providers.Embedder{"emb": r.emb}}, GlobalEmbed: globalSetting(""),
		Log: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return r.now },
	})
	ask := func() { rc.Recall(context.Background(), memory.Query{DeviceID: r.d1, Input: "hello", Embed: "emb"}) }
	count := func() int { return strings.Count(r.logs.String(), "reason=global_embed_unset") }
	ask()
	ask()
	if count() != 1 {
		t.Fatalf("log %s", r.logs.String())
	}
	r.now = r.now.Add(2 * time.Hour)
	ask()
	if count() != 2 {
		t.Fatalf("log %s", r.logs.String())
	}
}

// panicEmbeds panics with text, as a broken provider might
type panicEmbeds struct{}

func (panicEmbeds) Embed(context.Context, string) (providers.Embedder, error) {
	panic("SECRET-PANIC memory text")
}

// a panic in the recall's own goroutines narrows what it returns instead of
// taking the server down, and its value, which may carry text, is dropped
func TestRecallSurvivesPanics(t *testing.T) {
	t.Run("embedding", func(t *testing.T) {
		r := newRecallRig(t)
		r.add(memory.NewMemory{Text: "pinned", Pinned: true})
		rc := memory.NewRecaller(memory.RecallerDeps{Store: r.s, Embeds: panicEmbeds{}, Log: slog.New(slog.NewTextHandler(r.logs, nil))})
		res := rc.Recall(context.Background(), memory.Query{DeviceID: r.d1, Input: "hello", Embed: "emb"})
		if res.Skipped != memory.SkipError || !eq(recalledTexts(res), []string{"pinned"}) {
			t.Fatalf("%+v", res)
		}
		if strings.Contains(r.logs.String(), "SECRET") {
			t.Fatalf("log %s", r.logs.String())
		}
	})
	t.Run("pinned read", func(t *testing.T) {
		// a store that is not there panics on its first read
		rc := memory.NewRecaller(memory.RecallerDeps{Embeds: embeds{}})
		res := rc.Recall(context.Background(), memory.Query{DeviceID: ghostID, Input: "hello"})
		if res.Skipped != memory.SkipStore || len(res.Memories) != 0 {
			t.Fatalf("%+v", res)
		}
	})
}
