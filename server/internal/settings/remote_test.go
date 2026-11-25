package settings_test

import (
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

func TestRemoteTargetsKey(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.RemoteTargets != settings.RemoteTargetsAllow {
		t.Fatalf("default %q", got.RemoteTargets)
	}
	r.put("/settings/global", `{"remoteTargets":"room"}`)
	if got := r.effective(); got.RemoteTargets != settings.RemoteTargetsRoom {
		t.Fatalf("global %q", got.RemoteTargets)
	}
	r.put("/devices/"+r.dev+"/settings", `{"remoteTargets":"deny"}`)
	if got := r.effective(); got.RemoteTargets != settings.RemoteTargetsDeny {
		t.Fatalf("device over global %q", got.RemoteTargets)
	}
	r.put("/devices/"+r.dev+"/settings", `{"remoteTargets":null}`)
	if got := r.effective(); got.RemoteTargets != settings.RemoteTargetsRoom {
		t.Fatalf("device removed %q", got.RemoteTargets)
	}
	for _, body := range []string{`{"remoteTargets":"never"}`, `{"remoteTargets":"DENY"}`, `{"remoteTargets":true}`, `{"remoteTargets":""}`} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
	if st, b := r.do("PUT", "/settings/global", `{"remoteTargets":"others"}`); st != 422 {
		t.Fatalf("global: %d %s", st, b)
	}
}
