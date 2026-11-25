package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const btList = `{"devices":[
	{"id":"a1b2c3d4e5f6","name":"MacBook","kind":"computer","paired":true,"connected":true},
	{"id":"0123456789ab","name":"iPhone Артёма","kind":"phone","paired":true,"connected":false},
	{"id":"ba9876543210","name":"iPhone Маши","kind":"phone","paired":true,"connected":false}]}`

// btFake is a device's bluetooth capability: each command it got, and its answers
type btFake struct {
	calls []sent
	args  []map[string]any
	// errs answers a command by name with an error
	errs map[string]error
	lang string
}

func (f *btFake) deps(target tools.TargetFunc) tools.BluetoothDeps {
	return tools.BluetoothDeps{
		Target: target,
		Settings: func(context.Context, string) (settings.Values, error) {
			return settings.Values{Language: f.lang}, nil
		},
		Command: func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error) {
			v, ok := commands.VouchFrom(ctx)
			f.calls = append(f.calls, sent{device: deviceID + " " + capability, name: name, vouch: v, vouched: ok})
			var a map[string]any
			_ = json.Unmarshal(args, &a)
			f.args = append(f.args, a)
			if err := f.errs[name]; err != nil {
				return nil, err
			}
			switch name {
			case "list", "scan":
				return json.RawMessage(btList), nil
			case "pair_window":
				return json.RawMessage(`{"seconds":120}`), nil
			case "connect":
				return json.RawMessage(`{"connected":true}`), nil
			case "disconnect":
				return json.RawMessage(`{"connected":false}`), nil
			case "forget":
				return json.RawMessage(`{"forgotten":1}`), nil
			}
			return nil, devices.ErrUnknownCommand
		},
	}
}

func (f *btFake) names() []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.device + " " + c.name
	}
	return out
}

func btResult(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBluetoothPairPhoneSpeaksTheFixedConfirmation(t *testing.T) {
	for _, c := range []struct{ lang, want string }{
		{"en", "Bluetooth is on for two minutes: pick the speaker on your phone or computer."},
		{"ru-RU", "Блютус включён на две минуты: выбери колонку в телефоне или компьютере."},
	} {
		f := &btFake{lang: c.lang}
		out, e := run(t, tools.Bluetooth(f.deps(nil)), cadencev2.Trigger_TRIGGER_WAKE, `{"action":"pair_phone"}`)
		if e != nil {
			t.Fatal(e)
		}
		m := btResult(t, out)
		if m["say"] != c.want || m["seconds"] != float64(120) || m["device"] != nil {
			t.Fatalf("%s: %v", c.lang, m)
		}
		if !slices.Equal(f.names(), []string{"d1 bluetooth pair_window"}) || f.args[0]["sessionId"] != "s1" {
			t.Fatalf("%s: calls %q args %v", c.lang, f.names(), f.args)
		}
	}
}

// rule 7: every write needs a woken session and sends nothing without one; list is a read
func TestBluetoothWritesNeedAWokenSession(t *testing.T) {
	for _, args := range []string{`{"action":"pair_phone"}`, `{"action":"connect","id":"a1b2c3d4e5f6"}`,
		`{"action":"disconnect","name":"MacBook"}`, `{"action":"forget","all":true}`, `{"action":"scan"}`} {
		f := &btFake{lang: "ru"}
		_, e := run(t, tools.Bluetooth(f.deps(nil)), cadencev2.Trigger_TRIGGER_FOLLOW_UP, args)
		if e == nil || e.Code != "wake_required" || !strings.Contains(e.Hint, "сначала позови меня") || len(f.calls) != 0 {
			t.Fatalf("%s: %+v, sent %q", args, e, f.names())
		}
	}
	f := &btFake{}
	out, e := run(t, tools.Bluetooth(f.deps(nil)), cadencev2.Trigger_TRIGGER_FOLLOW_UP, `{"action":"list"}`)
	if e != nil || len(btResult(t, out)["devices"].([]any)) != 3 {
		t.Fatalf("list: %s %+v", out, e)
	}
	if _, ok := f.args[0]["sessionId"]; ok {
		t.Fatalf("list sent a session: %v", f.args[0])
	}
}

func TestBluetoothNameMatchesTheTargetsList(t *testing.T) {
	cases := []struct {
		args, want string
		cands      int
	}{
		{`{"action":"connect","name":"macbook"}`, "a1b2c3d4e5f6", 0},
		{`{"action":"connect","name":"айфон артема"}`, "not_found", 3},
		{`{"action":"disconnect","name":"iphone артема"}`, "0123456789ab", 0},
		{`{"action":"connect","name":"iPhone"}`, "ambiguous", 2},
		{`{"action":"forget","name":"mac"}`, "a1b2c3d4e5f6", 0},
		{`{"action":"connect"}`, "invalid_arguments", 0},
	}
	for _, c := range cases {
		f := &btFake{}
		out, e := run(t, tools.Bluetooth(f.deps(nil)), cadencev2.Trigger_TRIGGER_BUTTON, c.args)
		if e != nil {
			if e.Code != c.want || len(e.Candidates) != c.cands {
				t.Fatalf("%s: %+v", c.args, e)
			}
			continue
		}
		last := f.args[len(f.args)-1]
		if f.calls[0].name != "list" || last["id"] != c.want || btResult(t, out)["id"] != c.want {
			t.Fatalf("%s: calls %q args %v out %s", c.args, f.names(), f.args, out)
		}
	}
}

func TestBluetoothMapsTheDevicesCodes(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&commands.DeviceError{Code: "wake_required"}, "wake_required"},
		{&commands.DeviceError{Code: "unknown_device"}, "not_found"},
		{&commands.DeviceError{Code: "connect_failed"}, "connect_failed"},
		{&commands.DeviceError{Code: "receive_off"}, "receive_off"},
		{&commands.DeviceError{Code: "unavailable"}, "unavailable"},
		{&commands.DeviceError{Code: "something"}, "bluetooth_failed"},
		{commands.ErrOffline, "device_offline"},
		{commands.ErrTimeout, "timeout"},
		{devices.ErrUnknownCapability, "no_capability"},
		{errors.New("x"), "bluetooth_failed"},
	}
	for _, c := range cases {
		f := &btFake{errs: map[string]error{"connect": c.err}}
		_, e := run(t, tools.Bluetooth(f.deps(nil)), cadencev2.Trigger_TRIGGER_WAKE, `{"action":"connect","id":"a1b2c3d4e5f6"}`)
		if e == nil || e.Code != c.want {
			t.Fatalf("%v: %+v, want %s", c.err, e, c.want)
		}
	}
}

// the household: this receive-only speaker d1 and the midi in the bedroom, both with bluetooth; nothing sends
func btHousehold(context.Context) ([]agent.DeviceInfo, error) {
	caps := []string{agent.CapAudioOutput, agent.CapBluetooth}
	return []agent.DeviceInfo{
		{ID: "d1", Name: "Кухня", Room: "Кухня", Kind: agent.KindSpeaker, Online: true, Caps: caps},
		{ID: "midi", Name: "Спальня", Room: "Спальня", Kind: agent.KindSpeaker, Online: true, Caps: caps, RemoteTargets: "allow"},
	}, nil
}

// a write on another device carries the woken session's grant and names its target; a follow-up sends nothing there
func TestBluetoothOnAnotherDeviceVouchesForTheWokenSession(t *testing.T) {
	r := &agent.Resolver{Household: btHousehold}
	f := &btFake{}
	tool := tools.Bluetooth(f.deps(r.ResolveTarget))
	out, e := run(t, tool, cadencev2.Trigger_TRIGGER_WAKE, `{"action":"pair_phone","device":"Спальня"}`)
	if e != nil {
		t.Fatal(e)
	}
	m := btResult(t, out)
	woken := commands.Vouch{DeviceID: "d1", SessionID: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE}
	if len(f.calls) != 1 || f.calls[0].device != "midi bluetooth" || !f.calls[0].vouched || f.calls[0].vouch != woken {
		t.Fatalf("sent %+v", f.calls)
	}
	if m["device"] != "Спальня" || m["deviceId"] != "midi" || !strings.Contains(m["say"].(string), "pick Спальня") {
		t.Fatalf("result %v", m)
	}

	f.calls = nil
	if _, e := run(t, tool, cadencev2.Trigger_TRIGGER_FOLLOW_UP, `{"action":"forget","all":true,"device":"Спальня"}`); e == nil || e.Code != "wake_required" || len(f.calls) != 0 {
		t.Fatalf("follow-up: %+v, sent %+v", e, f.calls)
	}

	// no board sends yet: the output tier answers no_capability and sends nothing
	for _, args := range []string{`{"action":"scan"}`, `{"action":"pair","name":"JBL"}`, `{"action":"route","route":"both"}`} {
		f.calls = nil
		if _, e := run(t, tool, cadencev2.Trigger_TRIGGER_WAKE, args); e == nil || e.Code != "no_capability" || len(f.calls) != 0 {
			t.Fatalf("%s: %+v, sent %+v", args, e, f.calls)
		}
	}
}

// a scan inquires over the radio, so it is a write: it carries the turn's session for the device to hold it to
func TestBluetoothScanCarriesTheSession(t *testing.T) {
	f := &btFake{}
	if _, e := run(t, tools.Bluetooth(f.deps(nil)), cadencev2.Trigger_TRIGGER_WAKE, `{"action":"scan"}`); e != nil {
		t.Fatal(e)
	}
	if !slices.Equal(f.names(), []string{"d1 bluetooth.output scan"}) || f.args[0]["sessionId"] != "s1" {
		t.Fatalf("calls %q args %v", f.names(), f.args)
	}
}

// a dashboard turn (TRIGGER_BUTTON, no session) sends no sessionId at all: the device takes a write without one as
// the owner's own call, where an empty one broke its schema (regression: a dashboard chat's scan of the pi failed)
func TestBluetoothDashboardTurnSendsNoSession(t *testing.T) {
	f := &btFake{}
	reg := agent.NewRegistry(tools.Bluetooth(f.deps(nil)))
	offered := reg.Offer(nil, false, cadencev2.Trigger_TRIGGER_BUTTON)[0]
	for _, args := range []string{`{"action":"scan"}`, `{"action":"pair_phone"}`} {
		f.args = nil
		c := agent.Call{DeviceID: "d1", Trigger: cadencev2.Trigger_TRIGGER_BUTTON, Args: json.RawMessage(args)}
		if _, e := agent.Run(context.Background(), offered, c); e != nil {
			t.Fatalf("%s: %+v", args, e)
		}
		if _, ok := f.args[0]["sessionId"]; ok {
			t.Fatalf("%s sent a sessionId: %v", args, f.args[0])
		}
	}
}
