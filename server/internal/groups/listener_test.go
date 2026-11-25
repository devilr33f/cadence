package groups_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
)

// the leader schedules the three chirps 1.5 s apart, the leader first
func (r *rig) schedules() {
	r.cmds.answer = func(string) (json.RawMessage, error) {
		return json.Marshal(capschema.MediaGroupCalibration{Code: "scheduled", StartsInMs: 1500,
			Slots: []capschema.MediaGroupCalibrationSlot{
				{DeviceID: r.ids[0], OffsetMs: 0}, {DeviceID: strings.ToUpper(r.ids[1]), OffsetMs: 1500},
				{DeviceID: r.ids[2], OffsetMs: 3000}}})
	}
}

func (r *rig) schedule(groupID string) groups.CalibrationView {
	r.t.Helper()
	r.schedules()
	return decode[groups.CalibrationView](r.t, r.must(200, http.MethodPost, "/groups/"+groupID+"/calibrate", `{"mode":"listener"}`))
}

func heardBody(members ...string) string {
	return `{"members":[` + strings.Join(members, ",") + `]}`
}

func heard(id, code string, arrivalUs int) string {
	return fmt.Sprintf(`{"deviceId":%q,"code":%q,"arrivalUs":%d,"confidence":0.8}`, id, code, arrivalUs)
}

// listener mode schedules the chirps and keeps the schedule; the browser's arrivals, less each slot, centred on their
// mean, give the trims, stored and pushed as mics mode's; the id is used once
func TestListenerCalibrationApplies(t *testing.T) {
	r := newRig(t, 3)
	g := r.calibrationRig(t)
	got := r.schedule(g.ID)
	if got.Code != "scheduled" || got.Mode != "listener" || len(got.CalibrationID) != 32 || got.StartsInMs != 1500 ||
		len(got.Slots) != 3 || got.Slots[1].DeviceID != r.ids[1] || got.Slots[2].OffsetMs != 3000 || got.Applied ||
		len(got.Members) != 3 || got.Members[1].Code != "scheduled" || got.Members[1].TrimUs != 3_000 {
		t.Fatalf("scheduled = %+v", got)
	}
	if sent := r.cmds.sent(); len(sent) != 1 || sent[0] != r.ids[0]+` media.group calibrate {"mode":"listener","toleranceUs":2000}` {
		t.Fatalf("sent = %v", sent)
	}
	path := "/groups/" + g.ID + "/calibrations/" + got.CalibrationID
	// offsets 0, +10 ms, -4 ms: mean +2 ms
	body := heardBody(heard(r.ids[0], "ok", 0), heard(r.ids[1], "ok", 1_510_000), heard(r.ids[2], "ok", 2_996_000))
	applied := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, path, body))
	if applied.Code != "ok" || !applied.Applied || applied.Mode != "listener" || len(applied.Members) != 3 ||
		applied.Members[0].OffsetUs != -2_000 || applied.Members[0].TrimUs != 2_000 ||
		applied.Members[1].OffsetUs != 8_000 || applied.Members[1].TrimUs != -8_000 ||
		applied.Members[2].TrimUs != 6_000 || applied.Members[2].Confidence != 0.8 {
		t.Fatalf("applied = %+v", applied)
	}
	view := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+g.ID, ""))
	if view.Members[0].TrimUs != 2_000 || view.Members[1].TrimUs != -8_000 || view.Members[1].TrimMs != -8 {
		t.Fatalf("group = %+v", view.Members)
	}
	if lead, _ := r.devs.of(r.ids[0]); lead.TrimUs != 2_000 {
		t.Fatalf("leader config %+v", lead)
	}
	if status, b := r.call("x", http.MethodPost, path, body); status != 404 || !strings.Contains(b, `"expired"`) {
		t.Fatalf("second apply: %d %s", status, b)
	}
	if strings.Contains(r.logs.String(), "1510000") {
		t.Fatal("an arrival reached the log")
	}
}

// nothing applies while a slot member was not heard, a trim would pass the bounds, the id is another group's, used
// or expired, or the leader did not schedule
func TestListenerCalibrationAppliesNothingElse(t *testing.T) {
	r := newRig(t, 3)
	g := r.calibrationRig(t)
	apply := func(body string) (int, string) {
		c := r.schedule(g.ID)
		return r.call("x", http.MethodPost, "/groups/"+g.ID+"/calibrations/"+c.CalibrationID, body)
	}
	cases := []struct {
		name, body, code string
	}{
		{"weak", heardBody(heard(r.ids[0], "ok", 0), heard(r.ids[1], "ok", 1_500_000), heard(r.ids[2], "weak", 3_000_000)), "incomplete"},
		{"missing", heardBody(heard(r.ids[0], "ok", 0), heard(r.ids[1], "ok", 1_500_000)), "incomplete"},
		// offsets 0, 1.2 s, 0: the second's trim would be -800 ms
		{"range", heardBody(heard(r.ids[0], "ok", 0), heard(r.ids[1], "ok", 2_700_000), heard(r.ids[2], "ok", 3_000_000)), "out_of_range"},
	}
	for _, c := range cases {
		status, b := apply(c.body)
		got := decode[groups.CalibrationView](t, b)
		if status != 200 || got.Code != c.code || got.Applied {
			t.Fatalf("%s: %d %+v", c.name, status, got)
		}
		if c.name == "missing" && got.Members[2].Code != "missed" {
			t.Fatalf("missing: %+v", got.Members)
		}
	}
	view := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+g.ID, ""))
	if view.Members[0].TrimUs != 0 || view.Members[1].TrimUs != 3_000 || view.Members[2].TrimUs != 0 {
		t.Fatalf("trims moved: %+v", view.Members)
	}
	ok := heardBody(heard(r.ids[0], "ok", 0), heard(r.ids[1], "ok", 1_500_000), heard(r.ids[2], "ok", 3_000_000))
	// another group's path: refused, and the id stays for its own
	c := r.schedule(g.ID)
	if status, _ := r.call("x", http.MethodPost, "/groups/00000000-0000-4000-8000-000000000000/calibrations/"+c.CalibrationID, ok); status != 404 {
		t.Fatalf("other group: %d", status)
	}
	// a newer calibration of the group replaces the earlier
	newer := r.schedule(g.ID)
	if status, _ := r.call("x", http.MethodPost, "/groups/"+g.ID+"/calibrations/"+c.CalibrationID, ok); status != 404 {
		t.Fatalf("replaced id: %d", status)
	}
	r.advance(121 * time.Second)
	if status, b := r.call("x", http.MethodPost, "/groups/"+g.ID+"/calibrations/"+newer.CalibrationID, ok); status != 404 || !strings.Contains(b, "expired") {
		t.Fatalf("expired: %d %s", status, b)
	}
	if status, _ := r.call("x", http.MethodPost, "/groups/"+g.ID+"/calibrations/nope", ok); status != 404 {
		t.Fatalf("unknown id: %d", status)
	}
	// a duplicate member is refused before the id is used
	c = r.schedule(g.ID)
	dup := heardBody(heard(r.ids[0], "ok", 0), heard(r.ids[0], "ok", 0))
	if status, _ := r.call("x", http.MethodPost, "/groups/"+g.ID+"/calibrations/"+c.CalibrationID, dup); status != 422 {
		t.Fatalf("duplicate: %d", status)
	}
	if status, _ := r.call("x", http.MethodPost, "/groups/"+g.ID+"/calibrations/"+c.CalibrationID, ok); status != 200 {
		t.Fatalf("after the duplicate: %d", status)
	}
	// the leader answers no schedule, or one naming a stranger
	for _, answer := range []capschema.MediaGroupCalibration{
		{Code: "ok"},
		{Code: "scheduled", Slots: []capschema.MediaGroupCalibrationSlot{{DeviceID: "00000000-0000-4000-8000-000000000001"}}},
	} {
		r.cmds.answer = func(string) (json.RawMessage, error) { return json.Marshal(answer) }
		if status, b := r.call("x", http.MethodPost, "/groups/"+g.ID+"/calibrate", `{"mode":"listener"}`); status != 409 || !strings.Contains(b, `"failed"`) {
			t.Fatalf("%+v: %d %s", answer, status, b)
		}
	}
	if status, _ := r.call("x", http.MethodPost, "/groups/"+g.ID+"/calibrate", `{"mode":"loud"}`); status != 422 {
		t.Fatalf("unknown mode: %d", status)
	}
	// mics mode named sends what it always did
	r.cmds.answer = result("incomplete")
	before := len(r.cmds.sent())
	got := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrate", `{"mode":"mics"}`))
	if sent := r.cmds.sent(); got.Mode != "mics" || sent[before] != r.ids[0]+" media.group calibrate {\"toleranceUs\":2000}" {
		t.Fatalf("mics: %+v %v", got, sent[before:])
	}
}

// the auto flags are on by default, reach the leader's config (bitrate's never with pcm), and the view shows what the
// leader plays with now
func TestGroupAutoTune(t *testing.T) {
	r := newRig(t, 2)
	r.link(0, "10.0.0.10")
	r.link(1, "10.0.0.11")
	g := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", r.groupBody("Home", 0, 1)))
	if !g.AutoLatency || !g.AutoBitrate || !g.AutoWait || g.Tuning != nil {
		t.Fatalf("created %+v", g)
	}
	if lead, _ := r.devs.of(r.ids[0]); !lead.AutoLatency || !lead.AutoBitrate || !lead.AutoWait {
		t.Fatalf("leader %+v", lead)
	}
	body := strings.Replace(r.groupBody("Home", 0, 1), `"kind"`, `"pcm":true,"autoWait":false,"kind"`, 1)
	g = decode[groups.GroupView](t, r.must(200, http.MethodPut, "/groups/"+g.ID, body))
	if !g.AutoLatency || !g.AutoBitrate || g.AutoWait {
		t.Fatalf("updated %+v", g)
	}
	if lead, _ := r.devs.of(r.ids[0]); !lead.AutoLatency || lead.AutoBitrate || lead.AutoWait {
		t.Fatalf("leader with pcm %+v", lead)
	}
	r.devs.mu.Lock()
	r.devs.state[r.ids[0]] = json.RawMessage(`{"groupId":"` + g.ID + `","state":"playing","role":"leader","latencyMs":620,"waitMs":900}`)
	r.devs.state[r.ids[1]] = json.RawMessage(`{"groupId":"` + g.ID + `","state":"playing","role":"member","latencyMs":1000}`)
	r.devs.mu.Unlock()
	list := decode[[]groups.GroupView](t, r.must(200, http.MethodGet, "/groups", ""))
	if tu := list[0].Tuning; tu == nil || tu.LatencyMs != 620 || tu.BitrateKbps != 0 || tu.WaitMs != 900 {
		t.Fatalf("tuning %+v", list[0].Tuning)
	}
}

// a member the leader gave no slot (not connected) was not measured: the others heard ok still apply nothing, and it
// shows as offline
func TestListenerCalibrationNeedsEveryMember(t *testing.T) {
	r := newRig(t, 3)
	g := r.calibrationRig(t)
	r.cmds.answer = func(string) (json.RawMessage, error) {
		return json.Marshal(capschema.MediaGroupCalibration{Code: "scheduled", StartsInMs: 1500,
			Slots: []capschema.MediaGroupCalibrationSlot{{DeviceID: r.ids[0], OffsetMs: 0}, {DeviceID: r.ids[1], OffsetMs: 1500}}})
	}
	c := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrate", `{"mode":"listener"}`))
	body := heardBody(heard(r.ids[0], "ok", 0), heard(r.ids[1], "ok", 1_504_000))
	got := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrations/"+c.CalibrationID, body))
	if got.Code != "incomplete" || got.Applied || got.Members[2].Code != "offline" {
		t.Fatalf("applied = %+v", got)
	}
	view := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+g.ID, ""))
	if view.Members[0].TrimUs != 0 || view.Members[1].TrimUs != 3_000 {
		t.Fatalf("trims moved: %+v", view.Members)
	}
}

// boosts reach the leader by member id; a stranger, a repeat or a gain past 14 dB is refused before anything is sent
func TestCalibrateBoosts(t *testing.T) {
	r := newRig(t, 3)
	g := r.calibrationRig(t)
	path := "/groups/" + g.ID + "/calibrate"
	r.cmds.answer = result("incomplete")
	body := fmt.Sprintf(`{"boost":[{"deviceId":%q,"gainDb":6},{"deviceId":%q,"gainDb":14}]}`, strings.ToUpper(r.ids[1]), r.ids[2])
	r.must(200, http.MethodPost, path, body)
	gains, _ := json.Marshal(map[string]int{r.ids[1]: 6, r.ids[2]: 14})
	want := r.ids[0] + ` media.group calibrate {"boost":` + string(gains) + `,"toleranceUs":2000}`
	if sent := r.cmds.sent(); len(sent) != 1 || sent[0] != want {
		t.Fatalf("sent = %v\nwant %s", sent, want)
	}
	for _, bad := range []string{
		`{"boost":[{"deviceId":"00000000-0000-4000-8000-000000000009","gainDb":6}]}`,
		fmt.Sprintf(`{"boost":[{"deviceId":%q,"gainDb":6},{"deviceId":%q,"gainDb":12}]}`, r.ids[1], r.ids[1]),
		fmt.Sprintf(`{"boost":[{"deviceId":%q,"gainDb":15}]}`, r.ids[1]),
		fmt.Sprintf(`{"boost":[{"deviceId":%q,"gainDb":-1}]}`, r.ids[1]),
	} {
		if status, b := r.call("x", http.MethodPost, path, bad); status != 422 {
			t.Fatalf("%s: %d %s", bad, status, b)
		}
	}
	if sent := r.cmds.sent(); len(sent) != 1 {
		t.Fatalf("sent = %v", sent)
	}
	// listener mode names its mode beside the boost
	r.schedules()
	r.must(200, http.MethodPost, path, fmt.Sprintf(`{"mode":"listener","boost":[{"deviceId":%q,"gainDb":6}]}`, r.ids[2]))
	if sent := r.cmds.sent(); sent[1] != fmt.Sprintf(`%s media.group calibrate {"mode":"listener","boost":{%q:6},"toleranceUs":2000}`, r.ids[0], r.ids[2]) {
		t.Fatalf("sent = %v", sent[1])
	}
}

// a member whose rounds spread past 0.5 ms is unstable, whatever the leader's code: nothing applies
func TestCalibrateSpreadMakesUnstable(t *testing.T) {
	r := newRig(t, 3)
	g := r.calibrationRig(t)
	r.cmds.answer = result("ok",
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[0], Code: "ok", OffsetUs: -100, SpreadUs: 40, Ci95Us: 85, Rounds: 4, MicChannels: 6},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[1], Code: "ok", OffsetUs: 200, SpreadUs: 501, Rounds: 3},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[2], Code: "ok", OffsetUs: -100, SpreadUs: 10, Rounds: 4})
	got := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrate", ""))
	if got.Code != "incomplete" || got.Applied || got.Members[1].Code != "unstable" || got.Members[0].SpreadUs != 40 ||
		got.Members[0].Ci95Us != 85 || got.Members[0].Rounds != 4 || got.Members[0].MicChannels != 6 || got.Members[1].TrimUs != 3_000 {
		t.Fatalf("calibration = %+v", got)
	}
}

// a schedule with rounds: each member's offset rests on its round-0 slot; the speakers' own measure of the run is
// fetched from the leader and shown beside it, with each member's path to the listener, never applied
func TestListenerCalibrationWithRoundsAndSpeakerMics(t *testing.T) {
	r := newRig(t, 3)
	g := r.calibrationRig(t)
	outputState(r, 0, capschema.GroupOutputSpeaker, nil)
	outputState(r, 1, capschema.GroupOutputHDMI, nil)
	outputState(r, 2, capschema.GroupOutputBluetooth, nil)
	r.cmds.answer = func(string) (json.RawMessage, error) {
		slots := []capschema.MediaGroupCalibrationSlot{}
		for round := range 2 {
			for k := range 3 {
				slots = append(slots, capschema.MediaGroupCalibrationSlot{DeviceID: r.ids[k], OffsetMs: (round*3 + k) * 1200, Round: round, Code: uint32(k)})
			}
		}
		return json.Marshal(capschema.MediaGroupCalibration{Code: "scheduled", StartsInMs: 1500, RunID: 77, Rounds: 2,
			Chirp: &capschema.MediaGroupChirp{F0Hz: 800, F1Hz: 7000, DurationUs: 250000, FadeUs: 10000},
			Probe: &capschema.MediaGroupProbe{Kind: "gold", BandLoHz: 1000, BandHiHz: 6000, FadeUs: 10000}, Slots: slots})
	}
	c := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrate", `{"mode":"listener"}`))
	if c.Rounds != 2 || c.Chirp == nil || c.Chirp.F1Hz != 7000 || c.Probe == nil || c.Probe.Kind != "gold" ||
		c.Probe.BandLoHz != 1000 || c.Probe.BandHiHz != 6000 || c.Probe.FadeUs != 10000 || len(c.Slots) != 6 ||
		c.Slots[4].Round != 1 || c.Slots[4].Code != 1 {
		t.Fatalf("scheduled = %+v", c)
	}
	var asked string
	r.cmds.result = func(args string) (json.RawMessage, error) {
		asked = args
		return json.Marshal(capschema.MediaGroupCalibration{Code: "incomplete", ResidualUs: 30, RunID: 77, Members: []capschema.MediaGroupCalibrationMember{
			{DeviceID: r.ids[0], Code: "ok", OffsetUs: -1_000, SpreadUs: 20, MicChannels: 6},
			{DeviceID: r.ids[1], Code: "ok", OffsetUs: 1_000, SpreadUs: 30, MicChannels: 2},
			{DeviceID: r.ids[2], Code: "no_capture"},
		}})
	}
	// listener offsets from round 0: 0, +10 ms, -4 ms, mean +2 ms
	body := heardBody(
		`{"deviceId":"`+r.ids[0]+`","code":"ok","arrivalUs":0,"confidence":0.8,"spreadUs":50,"rounds":2}`,
		`{"deviceId":"`+r.ids[1]+`","code":"ok","arrivalUs":1210000,"confidence":0.8,"spreadUs":80,"rounds":2}`,
		`{"deviceId":"`+r.ids[2]+`","code":"ok","arrivalUs":2396000,"confidence":0.8,"spreadUs":90,"rounds":1}`)
	got := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrations/"+c.CalibrationID, body))
	if asked != `{"mode":"listener_result","runId":77}` {
		t.Fatalf("asked %s", asked)
	}
	m := got.Members
	// path: listener 0 and 10 ms less mics -1 and +1 ms is +1 and +9 ms, centred on +5: -4 and +4 ms
	if got.Code != "ok" || !got.Applied || m[1].OffsetUs != 8_000 || m[1].SpreadUs != 80 || m[1].Rounds != 2 ||
		m[0].MicsCode != "ok" || m[0].MicsOffsetUs != -1_000 || m[0].MicChannels != 6 || m[0].PathUs == nil || *m[0].PathUs != -4_000 ||
		m[1].PathUs == nil || *m[1].PathUs != 4_000 || m[2].MicsCode != "no_capture" || m[2].PathUs != nil ||
		got.MicsResidualUs == nil || *got.MicsResidualUs != 30 || got.Rounds != 2 {
		t.Fatalf("applied = %+v", got)
	}
	// the listener run's trims belong to the outputs its schedule snapshotted, not to a later route
	for i, want := range []capschema.MediaGroupTrim{{OutputID: "speaker", TrimUs: 2000}, {OutputID: "hdmi", TrimUs: -8000},
		{OutputID: "bt:aa:bb:cc:dd:ee:02", TrimUs: 6000}} {
		if got.Members[i].Output == nil || got.Members[i].Output.ID != want.OutputID {
			t.Errorf("result output %d = %+v", i, got.Members[i].Output)
		}
		cfg, _ := r.devs.of(r.ids[i])
		if len(cfg.Trims) != 1 || cfg.Trims[0] != want {
			t.Errorf("listener trim %d = %+v, want %+v", i, cfg.Trims, want)
		}
	}
	if strings.Contains(r.logs.String(), "1210000") {
		t.Fatal("an arrival reached the log")
	}
}

// the speakers' measure failing (an old leader, an expired run) leaves the apply as it was; an ok arrival spread past
// 0.5 ms is unstable and applies nothing
func TestListenerCalibrationWithoutSpeakerMics(t *testing.T) {
	r := newRig(t, 3)
	g := r.calibrationRig(t)
	schedule := func() groups.CalibrationView {
		r.cmds.answer = func(string) (json.RawMessage, error) {
			return json.Marshal(capschema.MediaGroupCalibration{Code: "scheduled", StartsInMs: 1500, RunID: 5, Rounds: 1,
				Slots: []capschema.MediaGroupCalibrationSlot{{DeviceID: r.ids[0]}, {DeviceID: r.ids[1], OffsetMs: 1200}, {DeviceID: r.ids[2], OffsetMs: 2400}}})
		}
		return decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrate", `{"mode":"listener"}`))
	}
	r.cmds.result = func(string) (json.RawMessage, error) { return nil, &commands.DeviceError{Code: "expired"} }
	c := schedule()
	ok := heardBody(heard(r.ids[0], "ok", 0), heard(r.ids[1], "ok", 1_200_000), heard(r.ids[2], "ok", 2_400_000))
	got := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrations/"+c.CalibrationID, ok))
	if got.Code != "ok" || !got.Applied || got.MicsResidualUs != nil || got.Members[0].MicsCode != "" || got.Members[0].PathUs != nil {
		t.Fatalf("applied = %+v", got)
	}
	c = schedule()
	spread := heardBody(heard(r.ids[0], "ok", 0), heard(r.ids[1], "ok", 1_200_000),
		`{"deviceId":"`+r.ids[2]+`","code":"ok","arrivalUs":2400000,"confidence":0.8,"spreadUs":700,"rounds":3}`)
	got = decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrations/"+c.CalibrationID, spread))
	if got.Code != "incomplete" || got.Applied || got.Members[2].Code != "unstable" {
		t.Fatalf("spread = %+v", got)
	}
}
