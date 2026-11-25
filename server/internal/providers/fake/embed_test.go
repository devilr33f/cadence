package fake_test

import (
	"context"
	"math"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/fake"
)

func newFakeEmbed(t *testing.T) providers.Embedder {
	t.Helper()
	var mod providers.Module
	for _, m := range fake.Modules() {
		if m.ID == "fake-embed" {
			mod = m
		}
	}
	if mod.Kind != providers.KindEmbed || len(mod.Secrets) != 0 {
		t.Fatalf("module %+v", mod)
	}
	v, err := mod.New([]byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	return v.(providers.Embedder)
}

func dot(a, b []float32) float64 {
	var d float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
	}
	return d
}

func TestFakeEmbedDeterministicNormalised(t *testing.T) {
	e := newFakeEmbed(t)
	a, err := e.Embed(context.Background(), []string{"Turn on the kitchen light", "turn ON the kitchen LIGHT!"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := e.Embed(context.Background(), []string{"turn on the kitchen light"})
	if len(a[0]) != 64 || e.Model() != "fake-embed" {
		t.Fatalf("dim %d model %q", len(a[0]), e.Model())
	}
	for i := range a[0] {
		if a[0][i] != b[0][i] || a[0][i] != a[1][i] {
			t.Fatal("not deterministic or case-sensitive")
		}
	}
	if n := dot(a[0], a[0]); math.Abs(n-1) > 1e-5 {
		t.Fatalf("norm %v", n)
	}
}

func TestFakeEmbedSimilarityOrdering(t *testing.T) {
	e := newFakeEmbed(t)
	v, err := e.Embed(context.Background(), []string{
		"the owner likes green tea", "green tea in the morning", "alarm at seven", "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dot(v[0], v[1]) <= dot(v[0], v[2]) {
		t.Fatalf("shared words not closer: %v vs %v", dot(v[0], v[1]), dot(v[0], v[2]))
	}
	if n := dot(v[3], v[3]); math.Abs(n-1) > 1e-5 {
		t.Fatalf("empty text not unit: %v", n)
	}
}

func TestFakeEmbedLimits(t *testing.T) {
	e := newFakeEmbed(t)
	if _, err := e.Embed(context.Background(), make([]string, 33)); err == nil || err.Error() != providers.CodeEmbedFailed {
		t.Fatalf("err %v", err)
	}
	if _, err := e.Embed(context.Background(), []string{strings.Repeat("x ", 9000)}); err != nil {
		t.Fatal(err)
	}
}
