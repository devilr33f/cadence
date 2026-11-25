package cec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"git.shino.boo/devilreef/cadence/device/internal/cec"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

func compile(t *testing.T, src string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("s.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("s.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// scanned is a controller over the standard fake, already scanned, with its state read back
func scanned(t *testing.T) (*cec.Controller, *cec.Fake) {
	t.Helper()
	fake := cec.NewFake()
	c := cec.New(fake, nil)
	c.Scan(context.Background())
	return c, fake
}

// state is the controller's latest state as a string
func state(t *testing.T, c *cec.Controller) string {
	t.Helper()
	select {
	case s := <-c.State():
		return string(s)
	default:
		t.Fatal("no state published")
		return ""
	}
}

func TestFramesRoundTrip(t *testing.T) {
	for _, b := range [][]byte{
		{0x40},                   // a poll
		{0x04, 0x44, 0x41},       // the tv's volume-up key to us
		{0x4f, 0x82, 0x20, 0x00}, // our active source broadcast
		{0x05, 0x70, 0x10, 0x00}, // a system audio mode request to the avr
		{0x0f, 0x72, 0x01},       // the avr's system audio broadcast
	} {
		f, err := cec.Parse(b)
		if err != nil {
			t.Fatalf("% x: %v", b, err)
		}
		if got := f.Bytes(); string(got) != string(b) {
			t.Fatalf("% x round-trips as % x", b, got)
		}
	}
}

func TestPhysicalAddressForms(t *testing.T) {
	for v, want := range map[uint16]string{0x0000: "0.0.0.0", 0x1000: "1.0.0.0", 0x2043: "2.0.4.3"} {
		if got := cec.FormatPhysical(v); got != want {
			t.Fatalf("format(%x) = %s, want %s", v, got, want)
		}
		if back, ok := cec.ParsePhysical(want); !ok || back != v {
			t.Fatalf("parse(%s) = %x %v, want %x", want, back, ok, v)
		}
	}
	if _, ok := cec.ParsePhysical("2.0.0"); ok {
		t.Fatal("three nibbles parse")
	}
	if _, ok := cec.ParsePhysical("2.0.0.16"); ok {
		t.Fatal("16 parses as a nibble")
	}
}

// the state the controller publishes must satisfy the schema both sides share, with a tv at 0 (its logical
// address is a value, never an omission) and an avr at 5
func TestStateMatchesSchema(t *testing.T) {
	c, _ := scanned(t)
	s := compile(t, capschema.DisplayCecState)
	published := state(t, c)
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(published))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(v); err != nil {
		t.Fatalf("state %s: %v", published, err)
	}
	if !strings.Contains(published, `"logicalAddress":0`) {
		t.Fatalf("state hides the tv's address 0: %s", published)
	}
}

func TestScanFindsTheWire(t *testing.T) {
	c, fake := scanned(t)
	res, err := c.Command(context.Background(), "refresh", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"name":"SIM TV"`, `"vendorId":240`, `"type":"tv"`, `"power":"on"`,
		`"name":"SIM AVR"`, `"type":"audio_system"`} {
		if !strings.Contains(string(res), want) {
			t.Fatalf("refresh %s misses %s", res, want)
		}
	}
	published := state(t, c)
	for _, want := range []string{`"systemAudio":{"available":true,"enabled":"off"}`, `"volumeTarget":"tv"`,
		`"adapter":{"present":true,"physicalAddress":"2.0.0.0"`} {
		if !strings.Contains(published, want) {
			t.Fatalf("state %s misses %s", published, want)
		}
	}
	if tv := fake.Snapshot(0); !tv.Present || tv.Power != "on" {
		t.Fatalf("tv = %v", tv)
	}
}

func TestPowerCommands(t *testing.T) {
	c, fake := scanned(t)
	if err := command(t, c, "power", `{"action":"off"}`); err != nil {
		t.Fatal(err)
	}
	if tv := fake.Snapshot(0); tv.Power != "standby" {
		t.Fatalf("tv power = %s", tv.Power)
	}
	if !strings.Contains(state(t, c), `"power":"standby"`) {
		t.Fatalf("state keeps the tv on: %s", state(t, c))
	}
	// toggle from standby wakes it and makes us its source
	if err := command(t, c, "power", `{"action":"toggle"}`); err != nil {
		t.Fatal(err)
	}
	if tv := fake.Snapshot(0); tv.Power != "on" {
		t.Fatalf("tv power = %s", tv.Power)
	}
	if fake.Active() != "2.0.0.0" {
		t.Fatalf("active source = %s", fake.Active())
	}
}

func TestVolumePressesTheTarget(t *testing.T) {
	c, fake := scanned(t)
	if err := command(t, c, "volume", `{"action":"up","steps":3}`); err != nil {
		t.Fatal(err)
	}
	if tv := fake.Snapshot(0); tv.Audio.Volume != 43 {
		t.Fatalf("tv volume = %d, want 43", tv.Audio.Volume)
	}
	if avr := fake.Snapshot(5); avr.Audio.Volume != 25 {
		t.Fatalf("avr volume = %d, want 25", avr.Audio.Volume)
	}
	// system audio on: the keys go to the avr, whose report the controller tracks
	if err := command(t, c, "system_audio", `{"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	if err := command(t, c, "volume", `{"action":"up"}`); err != nil {
		t.Fatal(err)
	}
	if avr := fake.Snapshot(5); avr.Audio.Volume != 26 {
		t.Fatalf("avr volume = %d, want 26", avr.Audio.Volume)
	}
	if !strings.Contains(state(t, c), `"volumeTarget":"audio_system"`) {
		t.Fatalf("state keeps the tv as target: %s", state(t, c))
	}
}

func TestMuteTogglesAndUnmuteNoPress(t *testing.T) {
	c, fake := scanned(t)
	if err := command(t, c, "volume", `{"action":"mute"}`); err != nil {
		t.Fatal(err)
	}
	if tv := fake.Snapshot(0); !tv.Audio.Muted {
		t.Fatal("tv not muted")
	}
	// unmute on a muted target presses the key; on a loud one it has nothing to do
	if err := command(t, c, "volume", `{"action":"unmute"}`); err != nil {
		t.Fatal(err)
	}
	if tv := fake.Snapshot(0); tv.Audio.Muted {
		t.Fatal("tv still muted")
	}
	before := fake.Snapshot(5)
	if err := command(t, c, "volume", `{"action":"unmute","steps":4}`); err != nil {
		t.Fatal(err)
	}
	if after := fake.Snapshot(5); after.Audio.Volume != before.Audio.Volume {
		t.Fatalf("avr volume moved %d -> %d on a loud unmute", before.Audio.Volume, after.Audio.Volume)
	}
}

func TestSystemAudioOff(t *testing.T) {
	c, _ := scanned(t)
	if err := command(t, c, "system_audio", `{"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	if err := command(t, c, "system_audio", `{"enabled":false}`); err != nil {
		t.Fatal(err)
	}
	published := state(t, c)
	if !strings.Contains(published, `"enabled":"off"`) {
		t.Fatalf("system audio still on: %s", published)
	}
	if !strings.Contains(published, `"volumeTarget":"tv"`) {
		t.Fatalf("target still the avr: %s", published)
	}
}

func TestDisabledConfigRefusesTransmits(t *testing.T) {
	c, _ := scanned(t)
	if err := c.Apply(context.Background(), 1, []byte(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Command(context.Background(), "power", []byte(`{"action":"off"}`)); err == nil || err.Error() != "command failed: disabled" {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Command(context.Background(), "refresh", []byte(`{}`)); err == nil || err.Error() != "command failed: disabled" {
		t.Fatalf("refresh err = %v", err)
	}
	if err := c.Apply(context.Background(), 2, []byte(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := command(t, c, "power", `{"action":"off"}`); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidArgsRefused(t *testing.T) {
	c, _ := scanned(t)
	for _, bad := range [][2]string{
		{"power", `{"action":"sideways"}`},
		{"power", `{}`},
		{"volume", `{"action":"up","steps":9}`},
		{"volume", `{"action":"up","extra":1}`},
		{"system_audio", `{}`},
		{"refresh", `{"x":1}`},
		{"active_source", `1`},
	} {
		if _, err := c.Command(context.Background(), bad[0], []byte(bad[1])); err == nil || err.Error() != "command failed: invalid" {
			t.Fatalf("%s %s: err = %v", bad[0], bad[1], err)
		}
	}
}

func TestWithoutABus(t *testing.T) {
	c := cec.New(nil, nil)
	c.Scan(context.Background())
	s := state(t, c)
	for _, want := range []string{`"present":false`, `"devices":[]`, `"available":false`, `"enabled":"unknown"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("state %s misses %s", s, want)
		}
	}
	if res, err := c.Command(context.Background(), "refresh", []byte(`{}`)); err != nil || string(res) != `{"devices":[]}` {
		t.Fatalf("refresh = %s %v", res, err)
	}
	if _, err := c.Command(context.Background(), "power", []byte(`{"action":"on"}`)); err == nil || err.Error() != "command failed: no_bus" {
		t.Fatalf("err = %v", err)
	}
}

// command runs one command, leaving its state for state() to read
func command(t *testing.T, c *cec.Controller, name, args string) error {
	t.Helper()
	_, err := c.Command(context.Background(), name, []byte(args))
	return err
}
