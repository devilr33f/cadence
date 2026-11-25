package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

type namedEmbedder struct{ name string }

func (namedEmbedder) Embed(context.Context, []string) ([][]float32, error) { return nil, nil }
func (n namedEmbedder) Model() string                                      { return n.name }

func TestGlobalEmbedderFollowsGlobalSetting(t *testing.T) {
	var o settings.Overrides
	var err error
	get := globalEmbedder(globalEmbedName(
		func(context.Context) (settings.Overrides, error) { return o, err }),
		func(_ context.Context, name string) (providers.Embedder, error) { return namedEmbedder{name}, nil },
	)
	ctx := context.Background()
	if _, e := get(ctx); e == nil {
		t.Fatal("no setting is no instance")
	}
	o = settings.Overrides{"embed": json.RawMessage(`"mem"`)}
	if e, gerr := get(ctx); gerr != nil || e.Model() != "mem" {
		t.Fatalf("%v %v", e, gerr)
	}
	o = settings.Overrides{"embed": json.RawMessage(`5`)}
	if _, e := get(ctx); e == nil {
		t.Fatal("a non-string is no instance")
	}
	err = errors.New("db")
	if _, e := get(ctx); e == nil {
		t.Fatal("a failed read is an error")
	}
}
