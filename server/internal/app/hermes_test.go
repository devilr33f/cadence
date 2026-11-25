package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/hermes"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

func TestHermesKindFollowsTheGlobalSetting(t *testing.T) {
	var o settings.Overrides
	var readErr error
	var built []string
	global := func(context.Context) (settings.Overrides, error) { return o, readErr }
	build := func(_ context.Context, name string) (*hermes.Client, error) {
		built = append(built, name)
		if name == "broken" {
			return nil, providers.ErrBuildFailed
		}
		return hermes.NewClient(hermes.ClientConfig{BaseURL: "https://h.example", APIKey: "k"})
	}
	ctx, log := context.Background(), slog.New(slog.DiscardHandler)
	if k, _ := hermesKind(ctx, global, build, nil, log); k != nil || len(built) != 0 {
		t.Fatalf("no setting built %v", built)
	}
	o = settings.Overrides{"hermes": json.RawMessage(`"agent"`)}
	if k, _ := hermesKind(ctx, global, build, nil, log); k == nil || k.Name() != "hermes" || len(built) != 1 || built[0] != "agent" {
		t.Fatalf("kind %v built %v", k, built)
	}
	o = settings.Overrides{"hermes": json.RawMessage(`"broken"`)}
	if k, _ := hermesKind(ctx, global, build, nil, log); k != nil {
		t.Fatal("a broken instance gave a kind")
	}
	readErr = errors.New("db down")
	if k, _ := hermesKind(ctx, global, build, nil, log); k != nil {
		t.Fatal("a failed read gave a kind")
	}
}
