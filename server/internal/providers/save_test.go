package providers_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

// rotating is a module whose build saves a renewed secret: its value is the save its build was given
type rotating struct{ save providers.SaveSecret }

func (rotating) Search(context.Context, string) ([]providers.SearchResult, error) { return nil, nil }

// a renewed secret is sealed back in place: the next build reads it, the other secrets stay, the row is the same; a
// build of an instance deleted and created again under its name cannot write over the new one
func TestSaveSecretRenewsTheInstance(t *testing.T) {
	ctx := context.Background()
	pool := storetest.Pool(t)
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	var seen []map[string]string
	mod := providers.Module{
		ID: "rotating", Kind: providers.KindSearch, ConfigSchema: `{"type":"object"}`,
		Secrets: []string{"cookies"}, OptionalSecrets: []string{"other"},
		Probe: func(context.Context, any) error { return nil },
		NewSaving: func(_ json.RawMessage, sec map[string]string, save providers.SaveSecret) (any, error) {
			seen = append(seen, sec)
			return rotating{save: save}, nil
		},
	}
	ins := providers.NewInstances(pool, box, providers.NewRegistry(false, mod))
	if _, err := ins.Create(ctx, "r", "rotating", nil, map[string]string{"cookies": "jar-1", "other": "kept"}); err != nil {
		t.Fatal(err)
	}
	v, err := ins.Search(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	save := v.(rotating).save
	if err := save(ctx, "cookies", "jar-2"); err != nil {
		t.Fatal(err)
	}
	if err := save(ctx, "unknown", "x"); err == nil {
		t.Fatal("saved a secret the module does not have")
	}
	// a fresh build (a probe builds afresh) reads the renewed jar and the other secret
	if code, err := ins.Probe(ctx, "r"); err != nil || code != "" {
		t.Fatalf("probe %q %v", code, err)
	}
	if last := seen[len(seen)-1]; last["cookies"] != "jar-2" || last["other"] != "kept" {
		t.Fatalf("rebuilt with %v", last)
	}
	list, err := ins.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	if b, _ := json.Marshal(list[0]); string(b) == "" || strings.Contains(string(b), "jar-") || strings.Contains(string(b), "kept") {
		t.Fatalf("a secret reached the listing: %s", b)
	}

	if err := ins.Delete(ctx, "r"); err != nil {
		t.Fatal(err)
	}
	if _, err := ins.Create(ctx, "r", "rotating", nil, map[string]string{"cookies": "new-jar"}); err != nil {
		t.Fatal(err)
	}
	if err := save(ctx, "cookies", "stale"); !errors.Is(err, providers.ErrNotFound) {
		t.Fatalf("an old build saved over the new instance: %v", err)
	}
	if _, err := ins.Probe(ctx, "r"); err != nil || seen[len(seen)-1]["cookies"] != "new-jar" {
		t.Fatalf("new instance's jar %v %v", seen[len(seen)-1], err)
	}
}
