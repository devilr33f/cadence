package groups_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
)

// fakeCommands is the leader's cadenced as calibrate-group reaches it: it answers what answer gives
type fakeCommands struct {
	mu     sync.Mutex
	calls  []string
	answer func(dev string) (json.RawMessage, error)
	// result answers listener_result, when set (else answer does)
	result func(args string) (json.RawMessage, error)
}

func (f *fakeCommands) Deliver(_ context.Context, dev, capability, name string, args []byte) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, dev+" "+capability+" "+name+" "+string(args))
	answer := f.answer
	result := f.result
	f.mu.Unlock()
	if result != nil && strings.Contains(string(args), `"listener_result"`) {
		return result(string(args))
	}
	if answer == nil {
		return nil, commands.ErrOffline
	}
	return answer(dev)
}

func (f *fakeCommands) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func result(code string, members ...capschema.MediaGroupCalibrationMember) func(string) (json.RawMessage, error) {
	return func(string) (json.RawMessage, error) {
		b, err := json.Marshal(capschema.MediaGroupCalibration{Code: code, ResidualUs: 7, Members: members})
		return b, err
	}
}

func (r *rig) calibrationRig(t *testing.T) groups.GroupView {
	t.Helper()
	r.link(0, "10.0.0.10")
	r.link(1, "10.0.0.11")
	r.link(2, "10.0.0.12")
	body := strings.Replace(r.groupBody("Pair", 0, 1, 2), `{"deviceId":"`+r.ids[1]+`"}`,
		`{"deviceId":"`+r.ids[1]+`","trimMs":3}`, 1)
	return decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", body))
}

func outputState(r *rig, i int, kind string, hint *int) {
	id := "other"
	switch kind {
	case capschema.GroupOutputSpeaker, capschema.GroupOutputHDMI:
		id = kind
	case capschema.GroupOutputBluetooth:
		id = "bt:aa:bb:cc:dd:ee:0" + fmt.Sprint(i)
	}
	st, _ := json.Marshal(capschema.MediaGroupStatus{State: "idle", Output: &capschema.MediaGroupOutput{
		ID: id, Kind: kind, DelayHintUs: hint,
	}})
	r.devs.state[r.ids[i]] = st
}

// an ok calibration sets each member's trim to the negation of its offset, to the microsecond, and pushes the configs
// with it; only the trims are stored
func TestCalibrateAppliesTheTrims(t *testing.T) {
	r := newRig(t, 3)
	g := r.calibrationRig(t)
	r.cmds.answer = result("ok",
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[0], Code: "ok", OffsetUs: -12_345, Confidence: 0.9},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[1], Code: "ok", OffsetUs: 16_500, Confidence: 0.8},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[2], Code: "ok", OffsetUs: -4_155, Confidence: 0.7})
	got := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrate", ""))
	if got.Code != "ok" || !got.Applied || got.ResidualUs != 7 || len(got.Members) != 3 || got.Members[0].TrimUs != 12_345 ||
		got.Members[1].TrimUs != -16_500 || got.Members[2].TrimUs != 4_155 {
		t.Fatalf("calibration = %+v", got)
	}
	if sent := r.cmds.sent(); len(sent) != 1 || sent[0] != r.ids[0]+" media.group calibrate {\"toleranceUs\":2000}" {
		t.Fatalf("sent = %v", sent)
	}
	view := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+g.ID, ""))
	if view.Members[0].TrimUs != 12_345 || view.Members[0].TrimMs != 12 || view.Members[1].TrimMs != -17 ||
		view.Members[2].TrimUs != 4_155 {
		t.Fatalf("group = %+v", view.Members)
	}
	lead, _ := r.devs.of(r.ids[0])
	member, _ := r.devs.of(r.ids[1])
	if lead.TrimUs != 12_345 || lead.TrimMs != 12 || member.TrimUs != -16_500 || member.TrimMs != -17 {
		t.Fatalf("configs: leader %+v member %+v", lead, member)
	}
	// a hand edit that sends the group back as read keeps the microseconds
	body := fmt.Sprintf(`{"name":"Pair","kind":"group","members":[{"deviceId":%q,"trimMs":12,"trimUs":12345},`+
		`{"deviceId":%q,"trimMs":-17,"trimUs":-16500},{"deviceId":%q,"trimMs":20}]}`, r.ids[0], r.ids[1], r.ids[2])
	edited := decode[groups.GroupView](t, r.must(200, http.MethodPut, "/groups/"+g.ID, body))
	if edited.Members[0].TrimUs != 12_345 || edited.Members[2].TrimUs != 20_000 || edited.Members[2].TrimMs != 20 {
		t.Fatalf("edited = %+v", edited.Members)
	}
}

func TestCalibrateAppliesTrimsToTheOutputsMeasured(t *testing.T) {
	r := newRig(t, 3)
	g := r.calibrationRig(t)
	hint := 145000
	outputState(r, 0, capschema.GroupOutputSpeaker, nil)
	outputState(r, 1, capschema.GroupOutputHDMI, nil)
	outputState(r, 2, capschema.GroupOutputBluetooth, &hint)
	r.cmds.answer = result("ok",
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[0], Code: "ok", OffsetUs: -12_345},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[1], Code: "ok", OffsetUs: 16_500},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[2], Code: "ok", OffsetUs: -4_155})
	got := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrate", ""))
	if !got.Applied || len(got.Members) != 3 || got.Members[0].Output == nil || got.Members[0].Output.ID != "speaker" ||
		got.Members[1].Output == nil || got.Members[1].Output.ID != "hdmi" || got.Members[2].Output == nil ||
		got.Members[2].Output.ID != "bt:aa:bb:cc:dd:ee:02" {
		t.Fatalf("outputs = %+v", got.Members)
	}
	sent := r.cmds.sent()
	if len(sent) != 1 {
		t.Fatalf("sent = %v", sent)
	}
	var args capschema.MediaGroupCalibrateArgs
	if err := json.Unmarshal([]byte(strings.SplitN(sent[0], " calibrate ", 2)[1]), &args); err != nil {
		t.Fatal(err)
	}
	for i, want := range []capschema.MediaGroupCalibratePrior{{PriorSpanUs: 150000}, {PriorUs: 80000, PriorSpanUs: 200000},
		{PriorUs: 145000, PriorSpanUs: 100000}} {
		if args.Priors[r.ids[i]] != want {
			t.Errorf("prior %d = %+v, want %+v", i, args.Priors[r.ids[i]], want)
		}
	}
	view := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+g.ID, ""))
	wantIDs := []string{"speaker", "hdmi", "bt:aa:bb:cc:dd:ee:02"}
	wantTrims := []int{12345, -16500, 4155}
	fallbacks := []int{0, 3000, 0}
	for i, m := range view.Members {
		if m.TrimUs != fallbacks[i] || len(m.Trims) != 1 || m.Trims[0].OutputID != wantIDs[i] || m.Trims[0].TrimUs != wantTrims[i] ||
			m.Status == nil || m.Status.Output == nil || m.Status.Output.ID != wantIDs[i] {
			t.Errorf("member %d = %+v", i, m)
		}
		cfg, _ := r.devs.of(m.DeviceID)
		if cfg.TrimUs != fallbacks[i] || len(cfg.Trims) != 1 || cfg.Trims[0] != (capschema.MediaGroupTrim{OutputID: wantIDs[i], TrimUs: wantTrims[i]}) {
			t.Errorf("config %d = %+v", i, cfg)
		}
	}
}

func TestCalibratePriorsCoverEveryOutputKind(t *testing.T) {
	r := newRig(t, 5)
	for i := range 5 {
		r.link(i, fmt.Sprintf("10.0.0.%d", i+10))
	}
	g := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", r.groupBody("Outputs", 0, 1, 2, 3, 4)))
	hint := 220000
	outputState(r, 0, capschema.GroupOutputSpeaker, nil)
	outputState(r, 1, capschema.GroupOutputHDMI, nil)
	outputState(r, 2, capschema.GroupOutputBluetooth, &hint)
	outputState(r, 3, capschema.GroupOutputBluetooth, nil)
	outputState(r, 4, capschema.GroupOutputOther, nil)
	r.cmds.answer = result("incomplete")
	r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrate", "")
	var args capschema.MediaGroupCalibrateArgs
	if err := json.Unmarshal([]byte(strings.SplitN(r.cmds.sent()[0], " calibrate ", 2)[1]), &args); err != nil {
		t.Fatal(err)
	}
	for i, want := range []capschema.MediaGroupCalibratePrior{{PriorSpanUs: 150000}, {PriorUs: 80000, PriorSpanUs: 200000},
		{PriorUs: 220000, PriorSpanUs: 100000}, {PriorUs: 200000, PriorSpanUs: 250000}, {PriorSpanUs: 250000}} {
		if args.Priors[r.ids[i]] != want {
			t.Errorf("prior %d = %+v, want %+v", i, args.Priors[r.ids[i]], want)
		}
	}
}

// a result that is not ok, or does not name every member measured, applies nothing; a refusal is its code
func TestCalibrateAppliesNothingElse(t *testing.T) {
	r := newRig(t, 3)
	g := r.calibrationRig(t)
	path := "/groups/" + g.ID + "/calibrate"
	r.cmds.answer = result("incomplete",
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[0], Code: "ok", OffsetUs: -100},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[1], Code: "ok", OffsetUs: 200},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[2], Code: "busy"})
	got := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, path, ""))
	if got.Code != "incomplete" || got.Applied || got.Members[1].TrimUs != 3_000 || got.Members[2].Code != "busy" {
		t.Fatalf("incomplete = %+v", got)
	}
	// ok, but a member is missing from it
	r.cmds.answer = result("ok",
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[0], Code: "ok", OffsetUs: -100},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[1], Code: "ok", OffsetUs: 100})
	if got := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, path, "")); got.Applied {
		t.Fatalf("partial ok applied: %+v", got)
	}
	view := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+g.ID, ""))
	if view.Members[0].TrimUs != 0 || view.Members[1].TrimUs != 3_000 {
		t.Fatalf("trims moved: %+v", view.Members)
	}
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{&commands.DeviceError{Code: "playing"}, 409, "playing"},
		{&commands.DeviceError{Code: "gated"}, 409, "gated"},
		{commands.ErrOffline, 409, "device_offline"},
		{commands.ErrTimeout, 504, "timeout"},
	} {
		r.cmds.answer = func(string) (json.RawMessage, error) { return nil, c.err }
		status, body := r.call("x", http.MethodPost, path, "")
		if status != c.status || !strings.Contains(body, `"`+c.code+`"`) {
			t.Fatalf("%v: %d %s", c.err, status, body)
		}
	}
	if status, _ := r.call("x", http.MethodPost, "/groups/00000000-0000-4000-8000-000000000000/calibrate", ""); status != 404 {
		t.Fatalf("unknown group: %d", status)
	}
}

// a stereo pair's coded probes must agree more tightly than a room group's
func TestStereoPairCalibrationUsesTightTolerance(t *testing.T) {
	r := newRig(t, 2)
	r.link(0, "10.0.0.10")
	r.link(1, "10.0.0.11")
	body := fmt.Sprintf(`{"name":"Pair","kind":"stereo_pair","members":[{"deviceId":%q,"channel":"left"},{"deviceId":%q,"channel":"right"}]}`, r.ids[0], r.ids[1])
	g := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", body))
	r.cmds.answer = result("incomplete")
	r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrate", "")
	if sent := r.cmds.sent(); len(sent) != 1 || sent[0] != r.ids[0]+` media.group calibrate {"toleranceUs":250}` {
		t.Fatalf("sent = %v", sent)
	}
}

// with no member online nothing can lead: no command goes out
func TestCalibrateNeedsALeader(t *testing.T) {
	r := newRig(t, 2)
	g := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", r.groupBody("Off", 0, 1)))
	r.cmds.answer = result("ok")
	status, body := r.call("x", http.MethodPost, "/groups/"+g.ID+"/calibrate", "")
	if status != 409 || !strings.Contains(body, "no_leader") || len(r.cmds.sent()) != 0 {
		t.Fatalf("%d %s, sent %v", status, body, r.cmds.sent())
	}
}
