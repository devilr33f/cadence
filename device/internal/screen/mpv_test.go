package screen_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

// noState: nothing new the state carries marker
func noState(t *testing.T, p capability.Provider, marker string) {
	t.Helper()
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case v := <-p.State():
			if strings.Contains(string(v), marker) {
				t.Fatalf("%s state %s, want no %s", p.Descriptor().ID, v, marker)
			}
		case <-deadline:
			return
		}
	}
}

func TestMediaMpvConfigCheckedKeptAndStateReported(t *testing.T) {
	r := newRig(t, screen.Options{})
	if r.caps[capschema.MediaMpvID] == nil {
		t.Fatal("media.mpv is not among the capabilities")
	}
	m := r.caps[capschema.MediaMpvID]
	state(t, m, `"screen":false`)
	// only the allowlisted keys; an mpv option name is not a key
	if err := m.Apply(context.Background(), 1, json.RawMessage(`{"vo":"x11"}`)); err == nil {
		t.Fatal("an option outside the allowlist applied")
	}
	if err := m.Apply(context.Background(), 2, json.RawMessage(`{"profile":"fast","hwdec":"mediacodec"}`)); err != nil {
		t.Fatal(err)
	}
	s := r.connect(t, screentest.Options{})
	cc := frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetCapabilityConfig().GetCapability() == capschema.MediaMpvID
	}).GetCapabilityConfig()
	if cc.GetJson() != `{"profile":"fast","hwdec":"mediacodec"}` {
		t.Fatalf("config = %v", cc)
	}
	state(t, m, `"screen":true`)

	// the screen's report becomes the capability's state
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_CapabilityState{CapabilityState: &screenv1.CapabilityState{
		Capability: capschema.MediaMpvID,
		Json:       `{"screen":true,"applied":["profile"],"pending":["hwdec"],"refused":["userAgent"]}`,
	}}})
	state(t, m, `"applied":["profile"],"pending":["hwdec"],"refused":["userAgent"]`)

	// a report that is not the state's schema is dropped, not published
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_CapabilityState{CapabilityState: &screenv1.CapabilityState{
		Capability: capschema.MediaMpvID, Json: `{"screen":true,"applied":["profile"],"nope":[]}`,
	}}})
	noState(t, m, `"nope"`)

	// another capability's report is not ours to take
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_CapabilityState{CapabilityState: &screenv1.CapabilityState{
		Capability: capschema.DisplayScreenID, Json: `{"screen":true,"applied":["bogus"]}`,
	}}})
	noState(t, m, `"bogus"`)

	// the screen goes: nothing applied, the config kept for the next one
	s.Close()
	state(t, m, `"screen":false,"applied":[],"pending":[],"refused":[]`)
	s2 := r.connect(t, screentest.Options{})
	cc2 := frame(t, s2, func(e *screenv1.Envelope) bool {
		return e.GetCapabilityConfig().GetCapability() == capschema.MediaMpvID
	}).GetCapabilityConfig()
	if cc2.GetJson() != `{"profile":"fast","hwdec":"mediacodec"}` {
		t.Fatalf("config after reconnect = %v", cc2)
	}
}
