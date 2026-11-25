package agent

import (
	"encoding/json"
	"testing"
)

func TestAPendingPlayIsNotASilentSuccess(t *testing.T) {
	calls := []called{{
		offered: true,
		tool:    Tool{Name: "music_play", SilentOnSuccess: true},
		out:     json.RawMessage(`{"pending":true,"starting":true,"startingOn":"Bedroom TV","stage":"extract"}`),
	}}
	if allSilent(calls) {
		t.Fatal("a pending play ended the turn without letting the model say it is starting")
	}
	calls[0].out = json.RawMessage(`{"playing":true}`)
	if !allSilent(calls) {
		t.Fatal("an immediate play still acts as the reply by itself")
	}
}
