package group_test

import (
	"encoding/json"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

func TestTrackingConfigAndPublishedState(t *testing.T) {
	r := newRig(t)
	r.g.GroupReady(r.send)
	cfg := `{"enabled":true,"groupId":"g-track","role":"leader","latencyMs":1000,"members":["m-1"],` +
		`"tracking":{"enabled":true,"members":["dev-self","m-1"]}}`
	if err := r.apply(t, cfg, secret); err != nil {
		t.Fatal(err)
	}
	validate(t, compile(t, capschema.MediaGroupConfig), cfg)
	r.g.SetOutput(capschema.MediaGroupOutput{ID: "speaker", Kind: capschema.GroupOutputSpeaker})
	sent := r.configs()
	last := sent[len(sent)-1]
	if !last.GetTrackingEnabled() || len(last.GetTrackingMembers()) != 2 || last.GetTrackingMembers()[0] != "dev-self" ||
		last.GetOutputKind() != capschema.GroupOutputSpeaker {
		t.Fatalf("tracking config = %v", last)
	}

	r.g.GroupEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_GroupTracked{GroupTracked: &audiodv1.GroupTracked{
		GroupId: "g-track", State: "tracking",
		Members: []*audiodv1.GroupTrackedMember{{
			MemberId: "m-1", OffsetUs: 1926, Ci95Us: 40, Cycles: 6, CorrectionUs: -1926, CorrectionId: 1,
		}},
	}}})
	state := r.state(t)
	validate(t, compile(t, capschema.MediaGroupState), state)
	want := `"tracking":{"state":"tracking","members":[{"deviceId":"m-1","offsetUs":1926,"ci95Us":40,"cycles":6,"correctionUs":-1926,"correctionId":1}]}`
	if !strings.Contains(state, want) {
		t.Fatalf("state = %s\nwant tracking %s", state, want)
	}

	// the id still names the last correction after its one-window pulse ends
	r.g.GroupEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_GroupTracked{GroupTracked: &audiodv1.GroupTracked{
		GroupId: "g-track", State: "tracking",
		Members: []*audiodv1.GroupTrackedMember{{MemberId: "m-1", OffsetUs: 900, Ci95Us: 40, Cycles: 6, CorrectionId: 1}},
	}}})
	state = r.state(t)
	validate(t, compile(t, capschema.MediaGroupState), state)
	if !strings.Contains(state, `"correctionId":1`) || strings.Contains(state, `"correctionUs"`) {
		t.Fatalf("next state = %s", state)
	}
}

func TestCalibrationCarriesUltrasonicCapability(t *testing.T) {
	r, _ := leaderWith(t, func(id string) *audiodv1.GroupCalibrated {
		return &audiodv1.GroupCalibrated{RequestId: id, Code: "ok", Members: []*audiodv1.GroupCalibratedMember{
			{MemberId: "dev-self", Code: "ok", Ultrasonic: "ok"},
			{MemberId: "m-1", Code: "ok", Ultrasonic: "weak"},
			{MemberId: "m-2", Code: "ok", Ultrasonic: "unsupported"},
		}}
	})
	raw, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result capschema.MediaGroupCalibration
	if json.Unmarshal(raw, &result) != nil {
		t.Fatalf("result = %s", raw)
	}
	if len(result.Members) != 3 || result.Members[0].Ultrasonic != "ok" || result.Members[1].Ultrasonic != "weak" ||
		result.Members[2].Ultrasonic != "unsupported" {
		t.Fatalf("result = %s", raw)
	}
}
