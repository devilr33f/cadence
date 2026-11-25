package app

import (
	"context"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

// without a kind there is nothing to start, so neither the tools nor the
// block are offered
func TestTaskToolsNeedAKind(t *testing.T) {
	values := func(context.Context, string) (settings.Values, error) { return settings.Defaults(), nil }
	m := tasks.NewManager(tasks.ManagerDeps{})
	if ts, board := taskTools(m, values); ts != nil || board != nil {
		t.Fatalf("tools %v board %v", ts, board)
	}
	m.Register(tasks.NewFakeKind("hermes"))
	ts, board := taskTools(m, values)
	if len(ts) != 5 || board == nil {
		t.Fatalf("tools %d board %v", len(ts), board)
	}
}

func TestTaskKindsShowARestartPending(t *testing.T) {
	now := "agent-b"
	kinds := taskKinds(func(context.Context) (string, error) { return now, nil }, "agent-a")
	got, err := kinds(context.Background())
	if err != nil || len(got) != 1 || got[0] != (tasks.KindStatus{Name: "hermes", Instance: "agent-a", Configured: "agent-b"}) {
		t.Fatalf("%+v %v", got, err)
	}
	off := taskKinds(func(context.Context) (string, error) { return "", nil }, "")
	if got, _ := off(context.Background()); got[0] != (tasks.KindStatus{Name: "hermes"}) {
		t.Fatalf("%+v", got)
	}
}
