package group_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// audiod as far as calibrate sees it: every GroupCalibrate it gets is answered by answer, through GroupEnvelope
type fakeAudiod struct {
	r      *rig
	answer func(id string) *audiodv1.GroupCalibrated

	mu         sync.Mutex
	requests   []string
	modes      []audiodv1.GroupCalibrateMode
	calibrates []*audiodv1.GroupCalibrate
}

func (f *fakeAudiod) send(env *audiodv1.Envelope) error {
	if c := env.GetGroupCalibrate(); c != nil {
		f.mu.Lock()
		f.requests = append(f.requests, c.GetRequestId())
		f.modes = append(f.modes, c.GetMode())
		f.calibrates = append(f.calibrates, c)
		f.mu.Unlock()
		if f.answer != nil {
			if a := f.answer(c.GetRequestId()); a != nil {
				go f.r.g.GroupEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_GroupCalibrated{GroupCalibrated: a}})
			}
		}
		return nil
	}
	return f.r.send(env)
}

func (f *fakeAudiod) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func leaderWith(t *testing.T, answer func(id string) *audiodv1.GroupCalibrated) (*rig, *fakeAudiod) {
	t.Helper()
	r := newRig(t)
	f := &fakeAudiod{r: r, answer: answer}
	r.g.GroupReady(f.send)
	if err := r.apply(t, leaderCfg, secret); err != nil {
		t.Fatal(err)
	}
	return r, f
}

func commandCode(t *testing.T, err error) string {
	t.Helper()
	var ce *capability.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want a command error", err)
	}
	return ce.Code
}

func TestDescriptorListsCalibrate(t *testing.T) {
	d := newRig(t).cap.Descriptor()
	if len(d.Commands) != 1 || d.Commands[capschema.GroupCalibrateCommand] != capschema.GroupCalibrateArgs {
		t.Fatalf("commands = %+v", d.Commands)
	}
	compile(t, capschema.GroupCalibrateArgs)
}

func TestCalibrateAnswersTheLeadersResult(t *testing.T) {
	r, f := leaderWith(t, func(id string) *audiodv1.GroupCalibrated {
		return &audiodv1.GroupCalibrated{RequestId: id, Code: "ok", ResidualUs: 12, Members: []*audiodv1.GroupCalibratedMember{
			{MemberId: "dev-self", Code: "ok", OffsetUs: -1500, Confidence: 0.9, Ci95Us: 120},
			{MemberId: "m-1", Code: "ok", OffsetUs: 1500, Confidence: 0.8},
			{MemberId: "m-2", Code: "busy"},
		}}
	})
	res, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var got capschema.MediaGroupCalibration
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != "ok" || got.ResidualUs != 12 || len(got.Members) != 3 || got.Members[0].DeviceID != "dev-self" ||
		got.Members[0].OffsetUs != -1500 || got.Members[0].Ci95Us != 120 || got.Members[1].OffsetUs != 1500 || got.Members[2].Code != "busy" {
		t.Fatalf("result = %+v", got)
	}
	if f.count() != 1 {
		t.Fatalf("GroupCalibrate sent %d times", f.count())
	}
}

func TestCalibrateRefusals(t *testing.T) {
	t.Run("a voice turn another device vouched for", func(t *testing.T) {
		r, f := leaderWith(t, nil)
		ctx := capability.WithVouched(t.Context(), capability.Vouched{
			CallerDeviceID: "other", SessionID: "s", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
		})
		_, err := r.cap.Command(ctx, capschema.GroupCalibrateCommand, nil)
		if commandCode(t, err) != "refused" || f.count() != 0 {
			t.Fatalf("err = %v, sent %d", err, f.count())
		}
	})
	for _, args := range []string{`{"level":100}`, `{"mode":"room"}`, `{"mode":null}`, `{"mode":"mics","x":1}`, `[]`,
		`{"mode":"listener_result"}`, `{"mode":"listener","runId":3}`, `{"runId":3}`, `{"boost":{"m-1":15}}`,
		`{"boost":{"m-1":-1}}`, `{"boost":{"":6}}`, `{"boost":null}`, `{"mode":"listener_result","runId":3,"boost":{"m-1":6}}`,
		`{"boost":{"a":1,"b":1,"c":1,"d":1,"e":1,"f":1,"g":1,"h":1,"i":1}}`} {
		t.Run("args "+args, func(t *testing.T) {
			r, f := leaderWith(t, nil)
			_, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, json.RawMessage(args))
			if commandCode(t, err) != "invalid" || f.count() != 0 {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("a member", func(t *testing.T) {
		r := newRig(t)
		r.g.GroupReady(r.send)
		if err := r.apply(t, memberCfg, secret); err != nil {
			t.Fatal(err)
		}
		_, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, nil)
		if commandCode(t, err) != "not_leader" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("audiod down", func(t *testing.T) {
		r := newRig(t)
		if err := r.apply(t, leaderCfg, secret); err != nil {
			t.Fatal(err)
		}
		_, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, nil)
		if commandCode(t, err) != "unavailable" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("refused before it ran", func(t *testing.T) {
		r, _ := leaderWith(t, func(id string) *audiodv1.GroupCalibrated {
			return &audiodv1.GroupCalibrated{RequestId: id, Code: "playing"}
		})
		_, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, nil)
		if commandCode(t, err) != "playing" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an answer that is no token", func(t *testing.T) {
		r, _ := leaderWith(t, func(id string) *audiodv1.GroupCalibrated {
			return &audiodv1.GroupCalibrated{RequestId: id, Code: "Bad Code"}
		})
		_, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, nil)
		if commandCode(t, err) != "failed" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no answer in time", func(t *testing.T) {
		r, _ := leaderWith(t, func(string) *audiodv1.GroupCalibrated { return nil })
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		_, err := r.cap.Command(ctx, capschema.GroupCalibrateCommand, nil)
		if commandCode(t, err) != "timeout" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("another calibration's answer is dropped", func(t *testing.T) {
		r, _ := leaderWith(t, func(id string) *audiodv1.GroupCalibrated {
			return &audiodv1.GroupCalibrated{RequestId: id + "x", Code: "ok"}
		})
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		_, err := r.cap.Command(ctx, capschema.GroupCalibrateCommand, nil)
		if commandCode(t, err) != "timeout" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCalibrateModes(t *testing.T) {
	for args, want := range map[string]audiodv1.GroupCalibrateMode{
		``:                    audiodv1.GroupCalibrateMode_GROUP_CALIBRATE_MODE_UNSPECIFIED,
		`{}`:                  audiodv1.GroupCalibrateMode_GROUP_CALIBRATE_MODE_UNSPECIFIED,
		`{"mode":"mics"}`:     audiodv1.GroupCalibrateMode_GROUP_CALIBRATE_MODE_UNSPECIFIED,
		`{"mode":"listener"}`: audiodv1.GroupCalibrateMode_GROUP_CALIBRATE_MODE_LISTENER,
	} {
		r, f := leaderWith(t, func(id string) *audiodv1.GroupCalibrated {
			return &audiodv1.GroupCalibrated{RequestId: id, Code: "too_few"}
		})
		_, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, json.RawMessage(args))
		if commandCode(t, err) != "too_few" || len(f.modes) != 1 || f.modes[0] != want {
			t.Fatalf("args %q: err = %v, modes %v", args, err, f.modes)
		}
		if args != "" {
			validate(t, compile(t, capschema.GroupCalibrateArgs), args)
		}
	}
}

func TestListenerCalibrationAnswersItsSchedule(t *testing.T) {
	r, _ := leaderWith(t, func(id string) *audiodv1.GroupCalibrated {
		return &audiodv1.GroupCalibrated{RequestId: id, Code: "scheduled", StartsInMs: 1500, Slots: []*audiodv1.GroupCalibrateSlot{
			{MemberId: "dev-self", OffsetMs: 0}, {MemberId: "m-1", OffsetMs: 1500}, {MemberId: "", OffsetMs: 9}, {MemberId: "m-2", OffsetMs: 3000},
		}}
	})
	res, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, json.RawMessage(`{"mode":"listener"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"scheduled","residualUs":0,"members":[],"startsInMs":1500,"slots":[{"deviceId":"dev-self","offsetMs":0,"round":0,"code":0},` +
		`{"deviceId":"m-1","offsetMs":1500,"round":0,"code":0},{"deviceId":"m-2","offsetMs":3000,"round":0,"code":0}]}`
	if string(res) != want {
		t.Fatalf("result = %s\nwant %s", res, want)
	}
}

func TestAutoTuneFlagsReachALeadersAudiod(t *testing.T) {
	r := newRig(t)
	r.g.GroupReady(r.send)
	cfg := `{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"autoLatency":true,"autoBitrate":true,"autoWait":true}`
	if err := r.apply(t, cfg, secret); err != nil {
		t.Fatal(err)
	}
	validate(t, compile(t, capschema.MediaGroupConfig), cfg)
	sent := r.configs()
	if len(sent) == 0 || !sent[len(sent)-1].GetAutoLatency() || !sent[len(sent)-1].GetAutoBitrate() || !sent[len(sent)-1].GetAutoWait() {
		t.Fatalf("sent = %v", sent)
	}
	// a member tunes nothing: the leader's flags alone mean anything
	member := `{"enabled":true,"groupId":"g","role":"member","leader":{"host":"10.0.0.2","port":7701},"latencyMs":800,"autoLatency":true}`
	if err := r.apply(t, member, secret); err != nil {
		t.Fatal(err)
	}
	sent = r.configs()
	if got := sent[len(sent)-1]; got.GetAutoLatency() || got.GetRole() != audiodv1.GroupRole_GROUP_ROLE_MEMBER {
		t.Fatalf("member sent = %v", got)
	}
}

func TestTrimUsReachesAudiod(t *testing.T) {
	r := newRig(t)
	r.g.GroupReady(r.send)
	cfg := `{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"trimMs":-2,"trimUs":-1840}`
	if err := r.apply(t, cfg, secret); err != nil {
		t.Fatal(err)
	}
	validate(t, compile(t, capschema.MediaGroupConfig), cfg)
	sent := r.configs()
	if len(sent) == 0 || sent[len(sent)-1].GetTrimUs() != -1840 || sent[len(sent)-1].GetTrimMs() != -2 {
		t.Fatalf("sent = %v", sent)
	}
	var ae *capability.ApplyError
	if err := r.apply(t, `{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"trimUs":500001}`, secret); !errors.As(err, &ae) {
		t.Fatalf("err = %v", err)
	}
}

func TestCalibrateCarriesBoostsAndRunID(t *testing.T) {
	var mu sync.Mutex
	var got *audiodv1.GroupCalibrate
	r := newRig(t)
	r.g.GroupReady(func(env *audiodv1.Envelope) error {
		if c := env.GetGroupCalibrate(); c != nil {
			mu.Lock()
			got = c
			mu.Unlock()
			go r.g.GroupEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_GroupCalibrated{GroupCalibrated: &audiodv1.GroupCalibrated{
				RequestId: c.GetRequestId(), Code: "expired"}}})
			return nil
		}
		return r.send(env)
	})
	if err := r.apply(t, leaderCfg, secret); err != nil {
		t.Fatal(err)
	}
	sent := func() *audiodv1.GroupCalibrate {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
	args := `{"mode":"listener","boost":{"m-2":14,"m-1":6}}`
	validate(t, compile(t, capschema.GroupCalibrateArgs), args)
	if _, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, json.RawMessage(args)); commandCode(t, err) != "expired" {
		t.Fatalf("err = %v", err)
	}
	if c := sent(); c.GetMode() != audiodv1.GroupCalibrateMode_GROUP_CALIBRATE_MODE_LISTENER || len(c.GetBoost()) != 2 ||
		c.GetBoost()[0].GetMemberId() != "m-1" || c.GetBoost()[0].GetGainDb() != 6 || c.GetBoost()[1].GetGainDb() != 14 {
		t.Fatalf("sent = %v", c)
	}
	args = `{"mode":"listener_result","runId":42}`
	validate(t, compile(t, capschema.GroupCalibrateArgs), args)
	if _, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, json.RawMessage(args)); commandCode(t, err) != "expired" {
		t.Fatalf("err = %v", err)
	}
	if c := sent(); c.GetMode() != audiodv1.GroupCalibrateMode_GROUP_CALIBRATE_MODE_LISTENER_RESULT || c.GetRunId() != 42 || len(c.GetBoost()) != 0 {
		t.Fatalf("sent = %v", c)
	}
}

func TestCalibrateCarriesToleranceAndPriors(t *testing.T) {
	r, f := leaderWith(t, func(id string) *audiodv1.GroupCalibrated {
		return &audiodv1.GroupCalibrated{RequestId: id, Code: "too_few"}
	})
	args := `{"toleranceUs":250,"priors":{"m-2":{"priorUs":380000,"priorSpanUs":200000},"m-1":{"priorSpanUs":150000}}}`
	validate(t, compile(t, capschema.GroupCalibrateArgs), args)
	_, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, json.RawMessage(args))
	if commandCode(t, err) != "too_few" || len(f.calibrates) != 1 {
		t.Fatalf("err = %v, requests %d", err, len(f.calibrates))
	}
	got := f.calibrates[0]
	if got.GetToleranceUs() != 250 || len(got.GetPriors()) != 2 || got.GetPriors()[0].GetMemberId() != "m-1" ||
		got.GetPriors()[0].GetPriorSpanUs() != 150000 || got.GetPriors()[1].GetMemberId() != "m-2" ||
		got.GetPriors()[1].GetPriorUs() != 380000 || got.GetPriors()[1].GetPriorSpanUs() != 200000 {
		t.Fatalf("sent = %v", got)
	}
}

func TestCalibrateWaitsPastCoresUsualWait(t *testing.T) {
	w, ok := newRig(t).cap.(capability.CommandWaiter)
	if !ok || w.CommandWait(capschema.GroupCalibrateCommand) < 45*time.Second || w.CommandWait("other") != 0 {
		t.Fatal("calibrate must be allowed at least 45 s")
	}
}

func TestListenerScheduleCarriesRoundsAndChirp(t *testing.T) {
	r, _ := leaderWith(t, func(id string) *audiodv1.GroupCalibrated {
		slots := []*audiodv1.GroupCalibrateSlot{}
		for round := range uint32(4) {
			for k, m := range []string{"dev-self", "m-1", "m-2", "m-3", "m-4", "m-5", "m-6", "m-7", "m-8"} {
				slots = append(slots, &audiodv1.GroupCalibrateSlot{MemberId: m, OffsetMs: (round*9 + uint32(k)) * 1200, Round: round, Code: uint32(k)}) //nolint:gosec // small
			}
		}
		return &audiodv1.GroupCalibrated{RequestId: id, Code: "scheduled", StartsInMs: 1500, RunId: 9, Rounds: 4, Slots: slots,
			Chirp: &audiodv1.GroupChirp{F0Hz: 800, F1Hz: 7000, DurationUs: 250000, FadeUs: 10000},
			Probe: &audiodv1.GroupProbe{Kind: audiodv1.GroupProbeKind_GROUP_PROBE_KIND_GOLD, BandLoHz: 1000, BandHiHz: 6000, FadeUs: 10000}}
	})
	res, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, json.RawMessage(`{"mode":"listener"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got capschema.MediaGroupCalibration
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatal(err)
	}
	if got.RunID != 9 || got.Rounds != 4 || got.Chirp == nil || got.Chirp.F0Hz != 800 || got.Chirp.DurationUs != 250000 ||
		got.Probe == nil || got.Probe.Kind != "gold" || got.Probe.BandLoHz != 1000 || got.Probe.BandHiHz != 6000 ||
		got.Probe.FadeUs != 10000 || len(got.Slots) != capschema.GroupCalibrateMaxSlots || got.Slots[10].Round != 1 ||
		got.Slots[10].DeviceID != "m-1" || got.Slots[10].Code != 1 {
		t.Fatalf("result = %+v", got)
	}
}

func TestCalibrationCarriesSpreadRoundsAndChannels(t *testing.T) {
	r, _ := leaderWith(t, func(id string) *audiodv1.GroupCalibrated {
		return &audiodv1.GroupCalibrated{RequestId: id, Code: "incomplete", RunId: 3, Rounds: 4, Members: []*audiodv1.GroupCalibratedMember{
			{MemberId: "dev-self", Code: "ok", OffsetUs: -10, Confidence: 0.9, SpreadUs: 40, Rounds: 4, MicChannels: 6},
			{MemberId: "m-1", Code: "unstable", OffsetUs: 10, SpreadUs: 900, Rounds: 2, MicChannels: 2},
		}}
	})
	res, err := r.cap.Command(t.Context(), capschema.GroupCalibrateCommand, json.RawMessage(`{"mode":"listener_result","runId":3}`))
	if err != nil {
		t.Fatal(err)
	}
	var got capschema.MediaGroupCalibration
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatal(err)
	}
	if got.RunID != 3 || got.Rounds != 4 || got.Members[0].SpreadUs != 40 || got.Members[0].MicChannels != 6 ||
		got.Members[1].Code != "unstable" || got.Members[1].Rounds != 2 {
		t.Fatalf("result = %+v", got)
	}
}
