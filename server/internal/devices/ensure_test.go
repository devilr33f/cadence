package devices_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

// Ensure stores a config with its secret as one generation, adds none when both are what is stored (jsonb's own
// spacing aside), and removes the secret for an empty value; the refusals are SetConfig's and the secret routes'
func TestEnsureSetsConfigAndSecretOnce(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(secretDesc()))
	ctx := context.Background()
	ensure := func(cfg, value string) bool {
		t.Helper()
		changed, err := e.svc.Ensure(ctx, e.id, "audio", json.RawMessage(cfg), "apiKey", value)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	if !ensure(`{"level":5}`, "k1") {
		t.Fatal("first ensure stored nothing")
	}
	if ensure(`{ "level": 5 }`, "k1") {
		t.Fatal("the same config and key stored again")
	}
	if !ensure(`{"level":5}`, "k2") || !ensure(`{"level":6}`, "k2") || !ensure(`{"level":6}`, "") || ensure(`{"level":6}`, "") {
		t.Fatal("a change was not stored, or an unchanged one was")
	}
	sets := e.hub.configSets()
	if len(sets) != 4 {
		t.Fatalf("%d sets", len(sets))
	}
	want := []struct {
		gen   int64
		level string
		key   string
	}{{1, "5", "k1"}, {2, "5", "k2"}, {3, "6", "k2"}, {4, "6", ""}}
	for i, w := range want {
		s := sets[i]
		if s.Generation != w.gen || !strings.Contains(s.Json, w.level) || s.Secrets["apiKey"] != w.key {
			t.Fatalf("set %d: gen %d json %s key set %v", i, s.Generation, s.Json, s.Secrets["apiKey"] != "")
		}
	}
	if strings.Contains(e.logs.String(), "k1") || strings.Contains(e.logs.String(), "k2") {
		t.Fatal("a key reached the log")
	}
	var invalid *devices.ConfigInvalidError
	if _, err := e.svc.Ensure(ctx, e.id, "audio", json.RawMessage(`{"level":500}`), "apiKey", "k"); !errors.As(err, &invalid) {
		t.Fatalf("invalid config: %v", err)
	}
	if _, err := e.svc.Ensure(ctx, e.id, "audio", json.RawMessage(`{}`), "nope", "k"); !errors.Is(err, devices.ErrUnknownSecret) {
		t.Fatalf("undeclared secret: %v", err)
	}
	if _, err := e.svc.Ensure(ctx, e.id, "media.group", json.RawMessage(`{}`), "groupKey", "k"); !errors.Is(err, devices.ErrUnknownCapability) {
		t.Fatalf("unknown capability: %v", err)
	}
	// offline: stored, pushed at the next hello
	e.hub.setOnline(false)
	if !ensure(`{"level":7}`, "k3") || len(e.hub.configSets()) != 4 {
		t.Fatal("an offline ensure was not stored, or was sent")
	}
}

// a reported state that differs from the stored one reaches OnState; the same again does not
func TestOnStateHearsChangedStates(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(desc("audio")))
	var heard []string
	e.svc.OnState(func(_, capability string, state json.RawMessage) { heard = append(heard, capability+string(state)) })
	for _, s := range []string{`{"playing":true}`, `{"playing": true}`, `{"playing":false}`} {
		e.svc.Handle(context.Background(), e.id, &cadencev2.Envelope{Body: &cadencev2.Envelope_StateDelta{
			StateDelta: &cadencev2.StateDelta{Capability: "audio", Json: s},
		}})
	}
	if len(heard) != 2 || heard[1] != `audio{"playing":false}` {
		t.Fatalf("heard %v", heard)
	}
}
