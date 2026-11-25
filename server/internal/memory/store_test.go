package memory_test

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/memory"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

const model = "embed-a"

type rig struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	s    *memory.Store
	d1   string
	d2   string
}

func newRig(t *testing.T) *rig {
	pool := storetest.Pool(t)
	q := db.New(pool)
	r := &rig{t: t, pool: pool, q: q, s: memory.New(pool)}
	for _, p := range []*string{&r.d1, &r.d2} {
		d, err := q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: rand.Text(), Model: "sim"})
		if err != nil {
			t.Fatal(err)
		}
		*p = d.ID.String()
	}
	return r
}

func (r *rig) add(n memory.NewMemory) memory.Memory {
	r.t.Helper()
	if n.Scope == "" {
		n.Scope = memory.ScopeGlobal
	}
	if n.Kind == "" {
		n.Kind = memory.KindFact
	}
	if n.Source == "" {
		n.Source = memory.SourceManual
	}
	if n.Status == "" {
		n.Status = memory.StatusActive
	}
	if n.Confidence == 0 {
		n.Confidence = 0.9
	}
	m, err := r.s.Add(context.Background(), n)
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

func texts(ms []memory.Memory) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Text)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAddGetRoundTrip(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	m := r.add(memory.NewMemory{Scope: memory.ScopeDevice, DeviceID: r.d1, Kind: memory.KindAlias, Text: "kitchen light is lamp 3",
		Vector: []float32{1, 0, 0}, EmbedModel: model, Pinned: true, Confidence: 0.75, Evidence: []int64{4, 9}, Source: memory.SourceLearned, Status: memory.StatusPending})
	got, err := r.s.Get(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != memory.ScopeDevice || got.DeviceID != r.d1 || got.Kind != memory.KindAlias || got.Text != "kitchen light is lamp 3" ||
		!got.Pinned || got.Confidence != 0.75 || len(got.Evidence) != 2 || got.Evidence[1] != 9 ||
		got.Source != memory.SourceLearned || got.Status != memory.StatusPending || got.EmbedModel != model || !got.HasEmbedding ||
		got.Hits != 0 || got.LastHitAt != nil || got.CreatedAt.IsZero() {
		t.Fatalf("got %+v", got)
	}
	g := r.add(memory.NewMemory{Text: "global one"})
	if g.DeviceID != "" || g.HasEmbedding || g.Evidence == nil {
		t.Fatalf("global %+v", g)
	}
	if _, err := r.s.Get(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.s.Get(ctx, "nope"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("bad id err = %v", err)
	}
}

func TestChecksRejectBadRows(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	base := memory.NewMemory{Scope: memory.ScopeGlobal, Kind: memory.KindFact, Text: "secret-ish text", Confidence: 0.5,
		Source: memory.SourceManual, Status: memory.StatusActive}
	cases := map[string]func(n *memory.NewMemory){
		"global with device": func(n *memory.NewMemory) { n.DeviceID = r.d1 },
		"device without id":  func(n *memory.NewMemory) { n.Scope = memory.ScopeDevice },
		"empty text":         func(n *memory.NewMemory) { n.Text = "" },
		"text 501":           func(n *memory.NewMemory) { n.Text = strings.Repeat("x", 501) },
		"bad scope":          func(n *memory.NewMemory) { n.Scope = "team" },
		"bad kind":           func(n *memory.NewMemory) { n.Kind = "rumor" },
		"bad source":         func(n *memory.NewMemory) { n.Source = "guess" },
		"bad status":         func(n *memory.NewMemory) { n.Status = "gone" },
		"confidence high":    func(n *memory.NewMemory) { n.Confidence = 1.1 },
		"confidence low":     func(n *memory.NewMemory) { n.Confidence = -0.1 },
		"vector no model":    func(n *memory.NewMemory) { n.Vector = []float32{1} },
		"model no vector":    func(n *memory.NewMemory) { n.EmbedModel = model },
	}
	for name, mut := range cases {
		n := base
		mut(&n)
		_, err := r.s.Add(ctx, n)
		if !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		} else if strings.Contains(err.Error(), "secret-ish") {
			t.Errorf("%s: error leaks text", name)
		}
	}
	n := base
	n.Text = strings.Repeat("я", 500)
	if _, err := r.s.Add(ctx, n); err != nil {
		t.Fatalf("500 code points: %v", err)
	}
}

func TestUpdatePatchesAndInvalidatesEmbedding(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	m := r.add(memory.NewMemory{Text: "old", Vector: []float32{1, 0}, EmbedModel: model})
	pin, status, conf := true, memory.StatusPending, float32(0.4)
	u, err := r.s.Update(ctx, m.ID, memory.Patch{Pinned: &pin, Status: &status, Confidence: &conf, Evidence: []int64{1}})
	if err != nil {
		t.Fatal(err)
	}
	if !u.Pinned || u.Status != memory.StatusPending || u.Confidence != 0.4 || u.Text != "old" || !u.HasEmbedding || len(u.Evidence) != 1 {
		t.Fatalf("u = %+v", u)
	}
	text, kind := "new", memory.KindLesson
	u, err = r.s.Update(ctx, m.ID, memory.Patch{Text: &text, Kind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	if u.Text != "new" || u.Kind != memory.KindLesson || u.HasEmbedding || u.EmbedModel != "" || !u.UpdatedAt.After(m.UpdatedAt) {
		t.Fatalf("u = %+v", u)
	}
	if err := r.s.SetEmbedding(ctx, m.ID, []float32{0, 1}, "embed-b"); err != nil {
		t.Fatal(err)
	}
	scope := memory.ScopeDevice
	u, err = r.s.Update(ctx, m.ID, memory.Patch{Scope: &scope, DeviceID: r.d2})
	if err != nil {
		t.Fatal(err)
	}
	if u.Scope != memory.ScopeDevice || u.DeviceID != r.d2 || !u.HasEmbedding || u.EmbedModel != "embed-b" {
		t.Fatalf("u = %+v", u)
	}
	bad := memory.ScopeGlobal
	if _, err := r.s.Update(ctx, m.ID, memory.Patch{Scope: &bad, DeviceID: r.d1}); !errors.Is(err, memory.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.s.Update(ctx, "00000000-0000-0000-0000-000000000000", memory.Patch{Pinned: &pin}); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := r.s.SetEmbedding(ctx, "00000000-0000-0000-0000-000000000000", []float32{1}, model); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestDelete(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	m := r.add(memory.NewMemory{Text: "x"})
	if err := r.s.Delete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Delete(ctx, m.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestListFilters(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.add(memory.NewMemory{Text: "g-fact"})
	r.add(memory.NewMemory{Text: "g-pref", Kind: memory.KindPreference, Pinned: true})
	r.add(memory.NewMemory{Text: "d1-fact", Scope: memory.ScopeDevice, DeviceID: r.d1})
	r.add(memory.NewMemory{Text: "d1-pend", Scope: memory.ScopeDevice, DeviceID: r.d1, Status: memory.StatusPending})
	r.add(memory.NewMemory{Text: "d2-fact", Scope: memory.ScopeDevice, DeviceID: r.d2})
	yes := true
	cases := []struct {
		name string
		f    memory.Filter
		want []string
	}{
		{"all", memory.Filter{}, []string{"g-fact", "g-pref", "d1-fact", "d1-pend", "d2-fact"}},
		{"global", memory.Filter{Scope: memory.ScopeGlobal}, []string{"g-fact", "g-pref"}},
		{"device", memory.Filter{Scope: memory.ScopeDevice}, []string{"d1-fact", "d1-pend", "d2-fact"}},
		{"device id", memory.Filter{DeviceID: r.d1}, []string{"d1-fact", "d1-pend"}},
		{"kind", memory.Filter{Kind: memory.KindPreference}, []string{"g-pref"}},
		{"pending", memory.Filter{Status: memory.StatusPending}, []string{"d1-pend"}},
		{"pinned", memory.Filter{Pinned: &yes}, []string{"g-pref"}},
	}
	for _, c := range cases {
		got, err := r.s.List(ctx, c.f)
		if err != nil {
			t.Fatal(err)
		}
		if !eq(texts(got), c.want) {
			t.Errorf("%s: got %v want %v", c.name, texts(got), c.want)
		}
	}
}

func TestNearestOrderThresholdScopesModelStatus(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	v := func(x, y float32) []float32 { return []float32{x, y} }
	r.add(memory.NewMemory{Text: "g-exact", Vector: v(1, 0), EmbedModel: model})
	r.add(memory.NewMemory{Text: "g-near", Vector: v(1, 1), EmbedModel: model})
	r.add(memory.NewMemory{Text: "g-far", Vector: v(0, 1), EmbedModel: model})
	r.add(memory.NewMemory{Text: "d1-close", Scope: memory.ScopeDevice, DeviceID: r.d1, Vector: v(3, 1), EmbedModel: model})
	r.add(memory.NewMemory{Text: "d2-exact", Scope: memory.ScopeDevice, DeviceID: r.d2, Vector: v(1, 0), EmbedModel: model})
	r.add(memory.NewMemory{Text: "other-model", Vector: v(1, 0), EmbedModel: "embed-b"})
	r.add(memory.NewMemory{Text: "pending", Vector: v(1, 0), EmbedModel: model, Status: memory.StatusPending})
	r.add(memory.NewMemory{Text: "no-vector"})

	q := []float32{1, 0}
	both, err := r.s.Nearest(ctx, q, model, memory.Scopes{Global: true, DeviceID: r.d1}, 10, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range both {
		names = append(names, s.Text)
	}
	if !eq(names, []string{"g-exact", "d1-close", "g-near"}) {
		t.Fatalf("both = %v", names)
	}
	if d := both[0].Similarity - 1; d > 1e-6 || d < -1e-6 {
		t.Fatalf("similarity = %v", both[0].Similarity)
	}
	if s := both[2].Similarity; s < 0.70 || s > 0.71 {
		t.Fatalf("near similarity = %v", s)
	}
	for _, c := range []struct {
		name string
		sc   memory.Scopes
		lim  int
		min  float64
		want []string
	}{
		{"global only", memory.Scopes{Global: true}, 10, 0.5, []string{"g-exact", "g-near"}},
		{"device only", memory.Scopes{DeviceID: r.d1}, 10, 0.5, []string{"d1-close"}},
		{"other device", memory.Scopes{DeviceID: r.d2}, 10, 0.5, []string{"d2-exact"}},
		{"no scopes", memory.Scopes{}, 10, 0, nil},
		{"limit", memory.Scopes{Global: true, DeviceID: r.d1}, 1, 0, []string{"g-exact"}},
		{"min 0 keeps far", memory.Scopes{Global: true}, 10, 0, []string{"g-exact", "g-near", "g-far"}},
	} {
		got, err := r.s.Nearest(ctx, q, model, c.sc, c.lim, c.min)
		if err != nil {
			t.Fatal(err)
		}
		var n []string
		for _, s := range got {
			n = append(n, s.Text)
		}
		if !eq(n, c.want) {
			t.Errorf("%s: got %v want %v", c.name, n, c.want)
		}
	}
	other, err := r.s.Nearest(ctx, q, "embed-b", memory.Scopes{Global: true}, 10, 0)
	if err != nil || len(other) != 1 || other[0].Text != "other-model" {
		t.Fatalf("other model = %v %v", other, err)
	}
	if _, err := r.s.Nearest(ctx, nil, model, memory.Scopes{Global: true}, 10, 0); err == nil {
		t.Fatal("empty vector accepted")
	}
}

func TestPinned(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.add(memory.NewMemory{Text: "g-pin", Pinned: true})
	r.add(memory.NewMemory{Text: "g-loose"})
	r.add(memory.NewMemory{Text: "g-pin-pending", Pinned: true, Status: memory.StatusPending})
	r.add(memory.NewMemory{Text: "d1-pin", Scope: memory.ScopeDevice, DeviceID: r.d1, Pinned: true})
	r.add(memory.NewMemory{Text: "d2-pin", Scope: memory.ScopeDevice, DeviceID: r.d2, Pinned: true})
	got, err := r.s.Pinned(ctx, memory.Scopes{Global: true, DeviceID: r.d1})
	if err != nil {
		t.Fatal(err)
	}
	if !eq(texts(got), []string{"g-pin", "d1-pin"}) {
		t.Fatalf("got %v", texts(got))
	}
	got, _ = r.s.Pinned(ctx, memory.Scopes{DeviceID: r.d2})
	if !eq(texts(got), []string{"d2-pin"}) {
		t.Fatalf("got %v", texts(got))
	}
}

func TestHit(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.add(memory.NewMemory{Text: "a"})
	b := r.add(memory.NewMemory{Text: "b"})
	c := r.add(memory.NewMemory{Text: "c"})
	if err := r.s.Hit(ctx, []string{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Hit(ctx, []string{a.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Hit(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{a.ID: 2, b.ID: 1, c.ID: 0} {
		m, _ := r.s.Get(ctx, id)
		if m.Hits != want || (want > 0) != (m.LastHitAt != nil) {
			t.Errorf("%s: hits=%d last=%v want %d", m.Text, m.Hits, m.LastHitAt, want)
		}
	}
	if err := r.s.Hit(ctx, []string{"bad"}); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestCountAndEviction(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	day := func(n int) string {
		return "now() - interval '" + string(rune('0'+n/10)) + string(rune('0'+n%10)) + " days'"
	}
	set := func(m memory.Memory, hits, ageDays int, hit bool) {
		r.t.Helper()
		last := "NULL"
		if hit {
			last = day(ageDays)
		}
		if _, err := r.pool.Exec(ctx, `UPDATE memories SET hits=$1, last_hit_at=`+last+`, created_at=`+day(ageDays)+` WHERE id=$2`, hits, m.ID); err != nil {
			r.t.Fatal(err)
		}
	}
	learned := func(n memory.NewMemory) memory.Memory {
		n.Source = memory.SourceLearned
		return r.add(n)
	}
	fresh := learned(memory.NewMemory{Text: "fresh-1-hit"})  // 1 * 1 = ~1
	old := learned(memory.NewMemory{Text: "old-10-hits"})    // 10 * 0.5^(28/14) = 2.5
	stale := learned(memory.NewMemory{Text: "stale-4-hits"}) // 4 * 0.5^(42/14) = 0.5
	never := learned(memory.NewMemory{Text: "never-hit"})    // 0
	pin := learned(memory.NewMemory{Text: "pinned", Pinned: true})
	pend := learned(memory.NewMemory{Text: "pending", Status: memory.StatusPending})
	dev := learned(memory.NewMemory{Text: "dev", Scope: memory.ScopeDevice, DeviceID: r.d1})
	manual := r.add(memory.NewMemory{Text: "manual"})
	set(fresh, 1, 0, true)
	set(old, 10, 28, true)
	set(stale, 4, 42, true)
	set(never, 0, 5, false)
	set(pin, 0, 90, false)
	set(pend, 0, 90, false)
	set(dev, 0, 90, false)
	set(manual, 0, 90, false)

	n, err := r.s.CountByScope(ctx, memory.ScopeGlobal, "")
	if err != nil || n != 6 {
		t.Fatalf("global count = %d %v", n, err)
	}
	n, _ = r.s.CountByScope(ctx, memory.ScopeDevice, r.d1)
	if n != 1 {
		t.Fatalf("d1 count = %d", n)
	}
	n, _ = r.s.CountByScope(ctx, memory.ScopeDevice, r.d2)
	if n != 0 {
		t.Fatalf("d2 count = %d", n)
	}
	// learned ones only: manual memories leave by hand
	got, err := r.s.EvictionCandidates(ctx, memory.ScopeGlobal, "", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !eq(texts(got), []string{"never-hit", "stale-4-hits", "fresh-1-hit", "old-10-hits"}) {
		t.Fatalf("order = %v", texts(got))
	}
	got, _ = r.s.EvictionCandidates(ctx, memory.ScopeGlobal, "", 2, nil)
	if !eq(texts(got), []string{"never-hit", "stale-4-hits"}) {
		t.Fatalf("limit = %v", texts(got))
	}
	got, _ = r.s.EvictionCandidates(ctx, memory.ScopeGlobal, "", 2, []string{never.ID})
	if !eq(texts(got), []string{"stale-4-hits", "fresh-1-hit"}) {
		t.Fatalf("keep = %v", texts(got))
	}
	got, _ = r.s.EvictionCandidates(ctx, memory.ScopeDevice, r.d1, 5, nil)
	if !eq(texts(got), []string{"dev"}) {
		t.Fatalf("device = %v", texts(got))
	}
	if _, err := r.s.EvictionCandidates(ctx, memory.ScopeGlobal, "", 2, []string{"bad"}); !errors.Is(err, memory.ErrInvalid) {
		t.Fatalf("bad keep id err = %v", err)
	}
}

func TestProposals(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	target := r.add(memory.NewMemory{Text: "lives in Bishkek", Source: memory.SourceLearned})
	prop := r.add(memory.NewMemory{Text: "lives in Almaty", Source: memory.SourceLearned, Status: memory.StatusPending,
		Replaces: target.ID, Proposal: memory.ProposalUpdate})
	got, err := r.s.Get(ctx, prop.ID)
	if err != nil || got.Replaces != target.ID || got.Proposal != memory.ProposalUpdate {
		t.Fatalf("proposal %+v %v", got, err)
	}
	if target.Replaces != "" || target.Proposal != "" {
		t.Fatalf("plain memory %+v", target)
	}
	for name, n := range map[string]memory.NewMemory{
		"active proposal":     {Text: "x", Source: memory.SourceLearned, Replaces: target.ID, Proposal: memory.ProposalUpdate},
		"proposal no target":  {Text: "x", Source: memory.SourceLearned, Status: memory.StatusPending, Proposal: memory.ProposalDelete},
		"target no proposal":  {Text: "x", Source: memory.SourceLearned, Status: memory.StatusPending, Replaces: target.ID},
		"unknown proposal op": {Text: "x", Source: memory.SourceLearned, Status: memory.StatusPending, Replaces: target.ID, Proposal: "merge"},
	} {
		n.Scope, n.Kind, n.Confidence = memory.ScopeGlobal, memory.KindFact, 0.5
		if n.Status == "" {
			n.Status = memory.StatusActive
		}
		if _, err := r.s.Add(ctx, n); !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	r.add(memory.NewMemory{Text: "forget it", Source: memory.SourceLearned, Status: memory.StatusPending,
		Replaces: target.ID, Proposal: memory.ProposalDelete})
	n, err := r.s.DeleteProposals(ctx, target.ID)
	if err != nil || n != 2 {
		t.Fatalf("deleted %d %v", n, err)
	}
	// deleting the memory takes its proposals with it
	again := r.add(memory.NewMemory{Text: "again", Source: memory.SourceLearned, Status: memory.StatusPending,
		Replaces: target.ID, Proposal: memory.ProposalUpdate})
	if err := r.s.Delete(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Get(ctx, again.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("proposal outlived its memory: %v", err)
	}
}

func TestPrunePending(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for i := range 5 {
		m := r.add(memory.NewMemory{Text: "p" + string(rune('0'+i)), Scope: memory.ScopeDevice, DeviceID: r.d1,
			Source: memory.SourceLearned, Status: memory.StatusPending})
		if _, err := r.pool.Exec(ctx, `UPDATE memories SET created_at = now() - make_interval(mins => $1) WHERE id = $2`, 10-i, m.ID); err != nil {
			t.Fatal(err)
		}
	}
	r.add(memory.NewMemory{Text: "manual pending", Scope: memory.ScopeDevice, DeviceID: r.d1, Status: memory.StatusPending})
	r.add(memory.NewMemory{Text: "d2 pending", Scope: memory.ScopeDevice, DeviceID: r.d2, Source: memory.SourceLearned, Status: memory.StatusPending})
	r.add(memory.NewMemory{Text: "global pending", Source: memory.SourceLearned, Status: memory.StatusPending})
	r.add(memory.NewMemory{Text: "active", Scope: memory.ScopeDevice, DeviceID: r.d1, Source: memory.SourceLearned})
	n, err := r.s.PrunePending(ctx, memory.ScopeDevice, r.d1, 3)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d %v", n, err)
	}
	all, _ := r.s.List(ctx, memory.Filter{})
	if !eq(texts(all), []string{"p2", "p3", "p4", "manual pending", "d2 pending", "global pending", "active"}) {
		t.Fatalf("left %v", texts(all))
	}
	if n, _ := r.s.PrunePending(ctx, memory.ScopeGlobal, "", 0); n != 1 {
		t.Fatalf("global pruned %d", n)
	}
}

func TestNearestWithPending(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	v := []float32{1, 0}
	a := r.add(memory.NewMemory{Text: "active", Vector: v, EmbedModel: model, Source: memory.SourceLearned})
	r.add(memory.NewMemory{Text: "pending", Vector: v, EmbedModel: model, Source: memory.SourceLearned, Status: memory.StatusPending})
	r.add(memory.NewMemory{Text: "proposal", Vector: v, EmbedModel: model, Source: memory.SourceLearned, Status: memory.StatusPending,
		Replaces: a.ID, Proposal: memory.ProposalUpdate})
	got, err := r.s.NearestWithPending(ctx, v, model, memory.Scopes{Global: true}, 10, -1)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Text)
	}
	// proposals stay out: they only restate a memory for the admin
	if slices.Sort(names); !eq(names, []string{"active", "pending"}) {
		t.Fatalf("with pending = %v", names)
	}
	only, _ := r.s.Nearest(ctx, v, model, memory.Scopes{Global: true}, 10, -1)
	if len(only) != 1 || only[0].Text != "active" {
		t.Fatalf("active only = %v", only)
	}
}

func TestStaleModel(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.add(memory.NewMemory{Text: "current", Vector: []float32{1}, EmbedModel: model})
	r.add(memory.NewMemory{Text: "old-model", Vector: []float32{1}, EmbedModel: "embed-old"})
	r.add(memory.NewMemory{Text: "none"})
	r.add(memory.NewMemory{Text: "pending-old", Vector: []float32{1}, EmbedModel: "embed-old", Status: memory.StatusPending})
	got, err := r.s.StaleModel(ctx, model, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !eq(texts(got), []string{"old-model", "none", "pending-old"}) {
		t.Fatalf("got %v", texts(got))
	}
	got, _ = r.s.StaleModel(ctx, model, 1)
	if len(got) != 1 {
		t.Fatalf("limit = %d", len(got))
	}
}

func TestCompactionsAndRuns(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.s.LatestCompaction(ctx, r.d1); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	var ids []string
	for i, sum := range []string{"first", "second", "third"} {
		c, err := r.s.AddCompaction(ctx, memory.NewCompaction{DeviceID: r.d1, UptoMessageID: int64(10 * (i + 1)), Summary: sum,
			OpenThreads: []byte(`["lamp"]`), Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if string(c.TaskRefs) != "[]" {
			t.Fatalf("task refs = %s", c.TaskRefs)
		}
		ids = append(ids, c.ID)
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := r.s.AddCompaction(ctx, memory.NewCompaction{DeviceID: r.d2, Summary: "other", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	latest, err := r.s.LatestCompaction(ctx, r.d1)
	if err != nil || latest.Summary != "third" || latest.UptoMessageID != 30 || string(latest.OpenThreads) != `["lamp"]` {
		t.Fatalf("latest = %+v %v", latest, err)
	}
	list, err := r.s.ListCompactions(ctx, r.d1, 2)
	if err != nil || len(list) != 2 || list[0].Summary != "third" || list[1].Summary != "second" {
		t.Fatalf("list = %+v %v", list, err)
	}

	mem := r.add(memory.NewMemory{Text: "m"})
	for i := range 2 {
		run, err := r.s.AddRun(ctx, memory.NewRun{CompactionID: ids[i], DeviceID: r.d1, Applied: i + 1, Dropped: 2, Pending: 3, MemoryIDs: []string{mem.ID}})
		if err != nil {
			t.Fatal(err)
		}
		if run.CompactionID != ids[i] || len(run.MemoryIDs) != 1 || run.MemoryIDs[0] != mem.ID {
			t.Fatalf("run = %+v", run)
		}
		time.Sleep(5 * time.Millisecond)
	}
	runs, err := r.s.ListRuns(ctx, r.d1, 10)
	if err != nil || len(runs) != 2 || runs[0].Applied != 2 || runs[1].Applied != 1 || runs[0].Dropped != 2 || runs[0].Pending != 3 {
		t.Fatalf("runs = %+v %v", runs, err)
	}
	if runs, _ := r.s.ListRuns(ctx, r.d2, 10); len(runs) != 0 {
		t.Fatalf("other device runs = %d", len(runs))
	}
}

func TestDeviceDeleteCascades(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.add(memory.NewMemory{Text: "d1-mem", Scope: memory.ScopeDevice, DeviceID: r.d1})
	r.add(memory.NewMemory{Text: "global"})
	c, err := r.s.AddCompaction(ctx, memory.NewCompaction{DeviceID: r.d1, Summary: "s", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.AddRun(ctx, memory.NewRun{CompactionID: c.ID, DeviceID: r.d1}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `DELETE FROM devices WHERE id = $1`, r.d1); err != nil {
		t.Fatal(err)
	}
	all, _ := r.s.List(ctx, memory.Filter{})
	if !eq(texts(all), []string{"global"}) {
		t.Fatalf("left %v", texts(all))
	}
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM conversation_compactions) + (SELECT count(*) FROM memory_learning_runs)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("left %d %v", n, err)
	}
}

func TestVectorValidation(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	nan, inf := float32(math.NaN()), float32(math.Inf(1))
	for name, v := range map[string][]float32{"zero": {0, 0}, "nan": {1, nan}, "inf": {inf, 1}, "empty": {}} {
		if _, err := r.s.Nearest(ctx, v, model, memory.Scopes{Global: true}, 5, 0); !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("nearest %s: %v", name, err)
		}
		if _, err := r.s.Add(ctx, memory.NewMemory{Scope: memory.ScopeGlobal, Kind: memory.KindFact, Text: "x", Confidence: 1,
			Source: memory.SourceManual, Status: memory.StatusActive, Vector: v, EmbedModel: model}); err == nil {
			t.Errorf("add %s accepted", name)
		}
	}
}

func TestDimensionMismatch(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.add(memory.NewMemory{Text: "a-2d", Vector: []float32{1, 0}, EmbedModel: model})
	r.add(memory.NewMemory{Text: "b-3d", Vector: []float32{1, 0, 0}, EmbedModel: "embed-b"})
	got, err := r.s.Nearest(ctx, []float32{1, 0}, model, memory.Scopes{Global: true}, 5, 0)
	if err != nil || len(got) != 1 || got[0].Text != "a-2d" {
		t.Fatalf("model a = %v %v", got, err)
	}
	got, err = r.s.Nearest(ctx, []float32{1, 0, 0}, "embed-b", memory.Scopes{Global: true}, 5, 0)
	if err != nil || len(got) != 1 || got[0].Text != "b-3d" {
		t.Fatalf("model b = %v %v", got, err)
	}
	r.add(memory.NewMemory{Text: "a-3d", Vector: []float32{1, 0, 0}, EmbedModel: model})
	_, err = r.s.Nearest(ctx, []float32{1, 0}, model, memory.Scopes{Global: true}, 5, 0)
	if !errors.Is(err, memory.ErrDimension) {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateDeviceWithoutScopeRejected(t *testing.T) {
	r := newRig(t)
	m := r.add(memory.NewMemory{Text: "x"})
	if _, err := r.s.Update(context.Background(), m.ID, memory.Patch{DeviceID: r.d1}); !errors.Is(err, memory.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrunePendingSparesPinned(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for i, pin := range []bool{true, false, false} {
		m := r.add(memory.NewMemory{Text: "p" + string(rune('0'+i)), Source: memory.SourceLearned, Status: memory.StatusPending, Pinned: pin})
		if _, err := r.pool.Exec(ctx, `UPDATE memories SET created_at = now() - make_interval(mins => $1) WHERE id = $2`, 10-i, m.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := r.s.PrunePending(ctx, memory.ScopeGlobal, "", 0); err != nil || n != 2 {
		t.Fatalf("pruned %d %v", n, err)
	}
	all, _ := r.s.List(ctx, memory.Filter{})
	if !eq(texts(all), []string{"p0"}) {
		t.Fatalf("left %v", texts(all))
	}
}
