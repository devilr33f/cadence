package agent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

func TestSeededModes(t *testing.T) {
	ctx := context.Background()
	modes := agent.NewModes(storetest.Pool(t), nil)
	want := []agent.Mode{
		{
			Name: "fast", Model: "deepseek/deepseek-v4.1-flash-1017", Reasoning: "none",
			MaxSteps: 8, MaxDuration: 20 * time.Second, Interim: agent.InterimDrop,
			Aliases: map[string][]string{"en": {"fast", "quick"}, "ru": {"быстрый"}},
		},
		{
			Name: "smart", Model: "", Reasoning: "medium",
			MaxSteps: 25, MaxDuration: 90 * time.Second, Interim: agent.InterimSpeak,
			Aliases: map[string][]string{"en": {"smart", "clever"}, "ru": {"умный"}},
		},
	}
	for _, w := range want {
		got, err := modes.Get(ctx, w.Name)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, w) {
			t.Fatalf("got %+v\nwant %+v", got, w)
		}
	}
	if _, err := modes.Get(ctx, "turbo"); !errors.Is(err, agent.ErrModeNotFound) {
		t.Fatalf("unknown mode: got %v", err)
	}
}

func TestContextWindow(t *testing.T) {
	for model, want := range map[string]int{
		"deepseek/deepseek-v4.1-flash-1017": 1_000_000,
		"some/unknown-model":                32768,
		"":                                  32768,
	} {
		if got := agent.ContextWindow(model); got != want {
			t.Errorf("ContextWindow(%q) = %d, want %d", model, got, want)
		}
	}
}

// a delete that meets a settings write naming its mode waits for that write,
// then finds the mode named and leaves it
func TestModeDeleteWaitsForASettingsWrite(t *testing.T) {
	ctx := context.Background()
	pool := storetest.Pool(t)
	modes := agent.NewModes(pool, nil)
	if _, err := modes.Put(ctx, agent.Mode{Name: "turbo", Reasoning: "low", MaxSteps: 3, MaxDuration: 30 * time.Second, Interim: agent.InterimSpeak}); err != nil {
		t.Fatal(err)
	}
	// a global write as settings makes one, open: under the lock, naming turbo
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if err := q.LockAgentModes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.PutSettings(ctx, db.PutSettingsParams{Scope: "global", Data: []byte(`{"allowedModes":["fast","smart","turbo"]}`)}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- modes.Delete(ctx, "turbo") }()
	select {
	case err := <-done:
		t.Fatalf("deleted beside an open settings write: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, agent.ErrModeInUse) {
		t.Fatalf("delete after the write: %v", err)
	}
	if _, err := modes.Get(ctx, "turbo"); err != nil {
		t.Fatalf("refused delete removed it: %v", err)
	}
}

// two puts take turns, so they cannot both take one free alias
func TestModePutWaitsForAnotherPut(t *testing.T) {
	ctx := context.Background()
	pool := storetest.Pool(t)
	modes := agent.NewModes(pool, nil)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if err := q.LockAgentModes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.PutAgentMode(ctx, db.PutAgentModeParams{
		Name: "turbo", Reasoning: "low", MaxSteps: 3, MaxSeconds: 30, Interim: "speak", Aliases: []byte(`{"en":["rapid"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := modes.Put(ctx, agent.Mode{
			Name: "swift", Reasoning: "low", MaxSteps: 3, MaxDuration: 30 * time.Second, Interim: agent.InterimSpeak,
			Aliases: map[string][]string{"en": {"Rapid"}},
		})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("put beside an open put: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var taken *agent.AliasTakenError
	if err := <-done; !errors.As(err, &taken) || taken.Lang != "en" || taken.Index != 0 {
		t.Fatalf("put after the other: %v", err)
	}
	if _, err := modes.Get(ctx, "swift"); !errors.Is(err, agent.ErrModeNotFound) {
		t.Fatalf("refused put stored: %v", err)
	}
}
