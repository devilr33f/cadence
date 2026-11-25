package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

// volumeFake is the server's copy of the state and a device that sets its own level.
type volumeFake struct {
	state    string
	hasState bool
	// the device's level, which set_level and change_level move
	level int
	// each command as "name args"
	calls []string
	// the device's answer: err, else result, else its level
	err    error
	result string
}

func (f *volumeFake) deps() tools.VolumeDeps {
	return tools.VolumeDeps{
		State: func(context.Context, string, string) (json.RawMessage, bool) {
			return json.RawMessage(f.state), f.hasState
		},
		Command: func(_ context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error) {
			if deviceID != "d1" || capability != "audio.output" {
				panic("wrong device or capability")
			}
			f.calls = append(f.calls, name+" "+string(args))
			if f.err != nil {
				return nil, f.err
			}
			if f.result != "" {
				return json.RawMessage(f.result), nil
			}
			var a struct {
				Level *int `json:"level"`
				Delta *int `json:"delta"`
			}
			_ = json.Unmarshal(args, &a)
			switch {
			case name == "set_level" && a.Level != nil:
				f.level = *a.Level
			case name == "change_level" && a.Delta != nil:
				f.level = min(max(f.level+*a.Delta, 0), 100)
			}
			return json.Marshal(map[string]int{"level": f.level})
		},
	}
}

func (f *volumeFake) wantCalls(t *testing.T, want ...string) {
	t.Helper()
	if len(f.calls) != len(want) {
		t.Fatalf("calls %q, want %q", f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("calls %q, want %q", f.calls, want)
		}
	}
}

func TestVolumeRaiseNeedsWake(t *testing.T) {
	f := &volumeFake{state: `{"level":40}`, hasState: true, level: 40}
	tool := tools.DeviceVolume(f.deps())
	for _, args := range []string{`{"level":41}`, `{"change":5}`} {
		_, e := run(t, tool, cadencev2.Trigger_TRIGGER_FOLLOW_UP, args)
		if e == nil || e.Code != "wake_required" {
			t.Fatalf("%s: got %+v", args, e)
		}
	}
	f.wantCalls(t)
	for _, trig := range []cadencev2.Trigger{cadencev2.Trigger_TRIGGER_WAKE, cadencev2.Trigger_TRIGGER_BUTTON} {
		if out, e := run(t, tool, trig, `{"level":60}`); e != nil || string(out) != `{"level":60}` {
			t.Fatalf("woken raise: %s %+v", out, e)
		}
	}
	f.wantCalls(t, `set_level {"level":60,"sessionId":"s1"}`, `set_level {"level":60,"sessionId":"s1"}`)
	// unknown level cannot rule out a raise
	g := &volumeFake{}
	if _, e := run(t, tools.DeviceVolume(g.deps()), cadencev2.Trigger_TRIGGER_FOLLOW_UP, `{"level":10}`); e == nil || e.Code != "wake_required" {
		t.Fatalf("unknown level: %+v", e)
	}
	g.wantCalls(t)
}

func TestVolumeUnspecifiedTriggerCannotRaise(t *testing.T) {
	f := &volumeFake{state: `{"level":40}`, hasState: true}
	tool := tools.DeviceVolume(f.deps())
	for _, args := range []string{`{"level":41}`, `{"change":1}`} {
		if _, e := run(t, tool, cadencev2.Trigger_TRIGGER_UNSPECIFIED, args); e == nil || e.Code != "wake_required" {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	f.wantCalls(t)
}

// the device judges the session too: a lower goes with the session id, so a
// raise the server's stale state hid is the device's to refuse
func TestVolumeLowerInFollowUp(t *testing.T) {
	f := &volumeFake{state: `{"level":40}`, hasState: true, level: 40}
	tool := tools.DeviceVolume(f.deps())
	for _, args := range []string{`{"level":10}`, `{"change":-100}`, `{"change":0}`} {
		if _, e := run(t, tool, cadencev2.Trigger_TRIGGER_FOLLOW_UP, args); e != nil {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	f.wantCalls(t, `set_level {"level":10,"sessionId":"s1"}`, `change_level {"delta":-100,"sessionId":"s1"}`,
		`change_level {"delta":0,"sessionId":"s1"}`)
}

// a change resolves on the device, against its own level under its lock: the
// server's copy may be stale, and "quieter" must never land above the level now
func TestVolumeChangeGoesToTheDevice(t *testing.T) {
	f := &volumeFake{state: `{"level":95}`, hasState: true, level: 30}
	tool := tools.DeviceVolume(f.deps())
	if out, e := run(t, tool, cadencev2.Trigger_TRIGGER_WAKE, `{"change":-10}`); e != nil || string(out) != `{"level":20}` {
		t.Fatalf("%s %+v", out, e)
	}
	// no server state is needed for a change
	f.hasState = false
	if out, e := run(t, tool, cadencev2.Trigger_TRIGGER_WAKE, `{"change":15}`); e != nil || string(out) != `{"level":35}` {
		t.Fatalf("%s %+v", out, e)
	}
	f.hasState, f.state = true, `{}`
	if _, e := run(t, tool, cadencev2.Trigger_TRIGGER_FOLLOW_UP, `{"change":-5}`); e != nil {
		t.Fatalf("%+v", e)
	}
	f.wantCalls(t, `change_level {"delta":-10,"sessionId":"s1"}`, `change_level {"delta":15,"sessionId":"s1"}`,
		`change_level {"delta":-5,"sessionId":"s1"}`)
}

func TestVolumeWithoutSessionSendsNone(t *testing.T) {
	f := &volumeFake{state: `{"level":40}`, hasState: true}
	reg := agent.NewRegistry(tools.DeviceVolume(f.deps()))
	for _, args := range []string{`{"level":20}`, `{"change":-5}`} {
		c := agent.Call{DeviceID: "d1", Trigger: cadencev2.Trigger_TRIGGER_UNSPECIFIED, Args: json.RawMessage(args)}
		if _, e := agent.Run(context.Background(), reg.Offer(nil, false, cadencev2.Trigger_TRIGGER_WAKE)[0], c); e != nil {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	f.wantCalls(t, `set_level {"level":20}`, `change_level {"delta":-5}`)
}

// the answer is the level the device set, which may differ from the one asked
func TestVolumeAnswersTheDevicesLevel(t *testing.T) {
	f := &volumeFake{state: `{"level":40}`, hasState: true, result: `{"level":35}`}
	tool := tools.DeviceVolume(f.deps())
	if out, e := run(t, tool, cadencev2.Trigger_TRIGGER_WAKE, `{"level":30}`); e != nil || string(out) != `{"level":35}` {
		t.Fatalf("%s %+v", out, e)
	}
	for _, bad := range []string{`null`, `{}`, `{"level":"x"}`} {
		f.result = bad
		if _, e := run(t, tool, cadencev2.Trigger_TRIGGER_WAKE, `{"level":30}`); e == nil || e.Code != "volume_failed" {
			t.Fatalf("%s: %+v", bad, e)
		}
	}
}

// an absolute level may be sent again; a change that may still land must not be
func TestVolumeMapsDeviceCodes(t *testing.T) {
	for _, c := range []struct {
		err       error
		code      string
		retryable bool
	}{
		{&commands.DeviceError{Code: "wake_required"}, "wake_required", false},
		{commands.ErrOffline, "device_offline", false},
		{commands.ErrTimeout, "timeout", true},
		{devices.ErrUnknownCommand, "unavailable", false},
		{devices.ErrUnknownCapability, "unavailable", false},
		{devices.ErrNotFound, "unavailable", false},
		{&commands.DeviceError{Code: "store_failed"}, "volume_failed", true},
		{commands.ErrBusy, "volume_failed", true},
		{errors.New("boom"), "volume_failed", true},
	} {
		for _, args := range []string{`{"level":30}`, `{"change":-5}`} {
			f := &volumeFake{state: `{"level":40}`, hasState: true, err: c.err}
			_, e := run(t, tools.DeviceVolume(f.deps()), cadencev2.Trigger_TRIGGER_WAKE, args)
			want := c.retryable && args == `{"level":30}`
			if e == nil || e.Code != c.code || e.Message == "" || e.Retryable != want {
				t.Fatalf("%v %s: %+v", c.err, args, e)
			}
			if w := agent.WakeRequired(); c.code == "wake_required" && (e.Message != w.Message || e.Hint != w.Hint) {
				t.Fatalf("wake refusal %+v", e)
			}
		}
	}
}

func TestVolumeExactlyOneArg(t *testing.T) {
	f := &volumeFake{state: `{"level":40}`, hasState: true}
	tool := tools.DeviceVolume(f.deps())
	for _, args := range []string{`{}`, `{"level":10,"change":-5}`, `{"level":101}`, `{"change":-101}`, `{"level":"x"}`} {
		if _, e := run(t, tool, cadencev2.Trigger_TRIGGER_WAKE, args); e == nil || e.Code != "invalid_arguments" {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	f.wantCalls(t)
	if !tool.SilentOnSuccess || tool.Effects != agent.Write {
		t.Fatal("volume must be a silent write")
	}
	// past the commands service's own 10 s, so its timeout is the one that answers
	if tool.Timeout <= 10*time.Second {
		t.Fatalf("timeout %v", tool.Timeout)
	}
}

func TestVolumeEventTurnCannotRaise(t *testing.T) {
	f := &volumeFake{state: `{"level":40}`, hasState: true, level: 40}
	tool := tools.DeviceVolume(f.deps())
	for _, args := range []string{`{"level":41}`, `{"change":5}`} {
		_, e := run(t, tool, cadencev2.Trigger_TRIGGER_EVENT, args)
		if e == nil || e.Code != "wake_required" {
			t.Fatalf("%s: got %+v", args, e)
		}
	}
	f.wantCalls(t)
}
