package settings_test

import (
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

func TestErrorSoundKey(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.ErrorSound != settings.ErrorSoundPrompt {
		t.Fatalf("default %q", got.ErrorSound)
	}
	r.put("/settings/global", `{"errorSound":"beep"}`)
	if got := r.effective(); got.ErrorSound != settings.ErrorSoundBeep {
		t.Fatalf("global %q", got.ErrorSound)
	}
	r.put("/devices/"+r.dev+"/settings", `{"errorSound":"off"}`)
	if got := r.effective(); got.ErrorSound != settings.ErrorSoundOff {
		t.Fatalf("device over global %q", got.ErrorSound)
	}
	r.put("/devices/"+r.dev+"/settings", `{"errorSound":null}`)
	if got := r.effective(); got.ErrorSound != settings.ErrorSoundBeep {
		t.Fatalf("device removed %q", got.ErrorSound)
	}
	for _, body := range []string{`{"errorSound":"loud"}`, `{"errorSound":"OFF"}`, `{"errorSound":true}`, `{"errorSound":""}`} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
	if st, b := r.do("PUT", "/settings/global", `{"errorSound":"spoken"}`); st != 422 {
		t.Fatalf("global: %d %s", st, b)
	}
}
