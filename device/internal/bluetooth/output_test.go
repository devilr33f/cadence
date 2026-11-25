package bluetooth_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth/btfake"
)

const farAddr = "AA:BB:CC:DD:EE:11"

func TestNoOutputUnlessAsked(t *testing.T) {
	h := newHarness(t)
	if h.b.OutputCapability() != nil {
		t.Fatal("bluetooth.output without its options")
	}
	h = newHarness(t, withOutput(""))
	if d := h.b.OutputCapability().Descriptor(); d.ID != "bluetooth.output" || len(d.Commands) != 6 {
		t.Fatalf("descriptor %+v", d)
	}
}

// a scan discovers br/edr devices and answers the speakers alone, nearest first; it changes nothing kept, so the
// server's tool sends it with no session, and one it names is held to it
func TestScan(t *testing.T) {
	h := newHarness(t, withOutput(""))
	h.waitCall("SetDiscoverable false")
	h.be.Nearby(farAddr, "Far", -80)
	h.be.Nearby(speakerAddr, speakerName, -50)
	h.be.Update(func(s *bluetooth.Snapshot) {
		s.Devices = append(s.Devices, bluetooth.Device{Path: btfake.DevicePath(phoneAddr), Address: phoneAddr, Name: phoneName,
			Icon: "phone", Class: 0x5a020c, RSSI: -40})
	})
	if _, err := h.output("scan", `{"sessionId":"follow-1"}`); code(err) != "wake_required" {
		t.Fatalf("follow-up scan: %v", err)
	}
	if _, err := h.output("scan", `{"seconds":31}`); code(err) != "invalid" {
		t.Fatalf("31 s: %v", err)
	}
	out, err := h.output("scan", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Devices []bluetooth.DeviceState `json:"devices"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	want := []bluetooth.DeviceState{
		{ID: h.idOf(speakerAddr), Name: speakerName, Kind: "speaker"},
		{ID: h.idOf(farAddr), Name: "Far", Kind: "speaker"},
	}
	if !slices.Equal(res.Devices, want) {
		t.Fatalf("devices %+v", res.Devices)
	}
	calls := h.be.Calls()
	i := slices.Index(calls, "SetDiscoveryFilter bredr")
	if i < 0 || !slices.Equal(calls[i:i+3], []string{"SetDiscoveryFilter bredr", "StartDiscovery", "StopDiscovery"}) {
		t.Fatalf("calls %q", calls)
	}
	h.waitLog(`"msg":"bluetooth_scan","found":2`)
	// an unpaired speaker is no speaker of the state
	if s := h.outputState(); s == nil || len(s["speakers"].([]any)) != 0 {
		t.Fatalf("state %v", s)
	}
}

// pair needs a woken session; our agent accepts the pairing we start, then the speaker is trusted and connected, it
// plays at no more than 40 and the output follows it
func TestPairASpeaker(t *testing.T) {
	h := newHarness(t, withOutput(""))
	h.waitCall("SetDiscoverable false")
	h.be.Nearby(speakerAddr, speakerName, -50)
	if _, err := h.output("scan", `{}`); err != nil {
		t.Fatal(err)
	}
	id := h.idOf(speakerAddr)
	for _, args := range []string{`{"id":"` + id + `","sessionId":"follow-1"}`, `{"id":"` + id + `","sessionId":"nobody"}`} {
		if _, err := h.output("pair", args); code(err) != "wake_required" {
			t.Fatalf("%s: %v", args, err)
		}
	}
	if _, err := h.output("pair", `{"id":"`+h.phoneID()+`","sessionId":"wake-1"}`); code(err) != "unknown_device" {
		t.Fatalf("an unknown id: %v", err)
	}
	out, err := h.output("pair", `{"id":"`+id+`","sessionId":"wake-1"}`)
	if err != nil || out != `{"paired":true,"connected":true}` {
		t.Fatalf("pair = %s, %v", out, err)
	}
	path := btfake.DevicePath(speakerAddr)
	// a reconcile that read the adapter still pairable mid-pair locks it once more, on its own goroutine, so its
	// calls land anywhere after the pair's own lock: that one lock counts
	var calls []string
	locked := false
	for _, c := range h.be.Calls() {
		lock := c == "SetPairable false" || c == "SetDiscoverable false"
		if lock && locked {
			continue
		}
		calls = append(calls, c)
		if c == "SetDiscoverable false" && slices.Contains(calls, "Paired "+path) {
			locked = true
		}
	}
	i := slices.Index(calls, "Pair "+path)
	// pairable (bluez's bondable) on just for the pair, under bluez's own timeout, then locked again
	if i < 2 || !slices.Equal(calls[i-2:i+6], []string{"SetPairableTimeout 60", "SetPairable true", "Pair " + path,
		"Paired " + path, "SetPairable false", "SetDiscoverable false", "SetTrusted true " + path, "Connect " + path}) {
		t.Fatalf("calls %q", calls)
	}
	if !h.be.Bonded(speakerAddr) {
		t.Fatal("the speaker paired without a bond")
	}
	h.waitLog(`"msg":"bluetooth_pair_accepted","id":"` + id + `","kind":"pairing","direction":"outgoing"`)
	h.waitRoute(speakerAddr)
	if evs := h.outEvents(); !slices.Equal(evs, []string{"route ", "lower 40", "route " + speakerAddr}) {
		t.Fatalf("events %q", evs)
	}
	waitFor(t, "the active speaker", func() bool {
		s := h.outputState()
		sp, _ := s["speakers"].([]any)
		return s != nil && s["active"] == id && len(sp) == 1
	})
	// the pairing we started is over: an incoming one outside a window is refused as before
	if h.be.PairIncoming(farAddr, "Far") {
		t.Fatal("an incoming pairing accepted outside a window")
	}
}

// the output follows the connected speakers: the most recently connected, else the config's, else set_route's
func TestOutputFollowsTheSpeaker(t *testing.T) {
	known := filepath.Join(t.TempDir(), "bluetooth", "speakers")
	h := newHarness(t, withOutput(known))
	h.be.AddSpeaker(speakerAddr, speakerName, true)
	h.waitRoute(speakerAddr)
	h.be.Drop(speakerAddr)
	h.waitRoute("")
	h.be.AddSpeaker(farAddr, "Far", true)
	h.waitRoute(farAddr)
	a, far := h.idOf(speakerAddr), h.idOf(farAddr)
	// the speaker that connects last plays
	if out, err := h.output("connect", `{"id":"`+a+`","sessionId":"wake-1"}`); err != nil || out != `{"connected":true}` {
		t.Fatalf("connect = %s, %v", out, err)
	}
	h.waitRoute(speakerAddr)
	if evs := h.outEvents(); strings.Count(strings.Join(evs, "|"), "lower 40") != 2 {
		t.Fatalf("each new speaker lowered once: %q", evs)
	}
	// the config's speaker wins over the latest
	if err := h.b.OutputCapability().Apply(t.Context(), 1, json.RawMessage(`{"speaker":"`+far+`"}`)); err != nil {
		t.Fatal(err)
	}
	h.waitRoute(farAddr)
	if err := h.b.OutputCapability().Apply(t.Context(), 2, json.RawMessage(`{"speaker":"nope"}`)); err == nil {
		t.Fatal("a bad speaker id applied")
	}
	// set_route's choice wins over the config's
	if _, err := h.output("set_route", `{"route":"bluetooth","id":"`+a+`","sessionId":"follow-1"}`); code(err) != "wake_required" {
		t.Fatalf("set_route in a follow-up: %v", err)
	}
	for _, route := range []string{"local", "both"} {
		if _, err := h.output("set_route", `{"route":"`+route+`","sessionId":"wake-1"}`); code(err) != "unsupported" {
			t.Fatalf("%s: %v", route, err)
		}
	}
	if out, err := h.output("set_route", `{"route":"bluetooth","id":"`+a+`","sessionId":"wake-1"}`); err != nil ||
		out != `{"route":"bluetooth","id":"`+a+`"}` {
		t.Fatalf("set_route = %s, %v", out, err)
	}
	h.waitRoute(speakerAddr)
	// a phone is bluetooth's, not the output's
	h.be.AddPhone(phoneAddr, phoneName)
	if _, err := h.output("disconnect", `{"id":"`+h.phoneID()+`","sessionId":"wake-1"}`); code(err) != "unknown_device" {
		t.Fatalf("a phone through the output: %v", err)
	}
	// bluealsa gone: no pcm to play to
	h.be.SetBluealsa(false)
	h.waitRoute("")
	if s := h.outputState(); s["active"] != "" || s["code"] != "bluealsa_missing" {
		t.Fatalf("state %v", s)
	}
	h.be.SetBluealsa(true)
	if _, err := h.output("disconnect", `{"id":"`+a+`","sessionId":"wake-1"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.output("forget", `{"id":"`+far+`","sessionId":"wake-1"}`); err != nil {
		t.Fatal(err)
	}
	// both, in the order they were last active (racing the disconnect's route here)
	raw, err := os.ReadFile(known)
	lines := strings.Fields(string(raw))
	slices.Sort(lines)
	if err != nil || !slices.Equal(lines, sorted(a, far)) || !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("known %q %v", raw, err)
	}
	// a speaker active here before is not lowered again, across restarts
	h2 := newHarness(t, withOutput(known))
	h2.be.AddSpeaker(speakerAddr, speakerName, true)
	h2.waitRoute(speakerAddr)
	if evs := h2.outEvents(); slices.Contains(evs, "lower 40") {
		t.Fatalf("a known speaker lowered: %q", evs)
	}
}

func sorted(ids ...string) []string {
	slices.Sort(ids)
	return ids
}

// the output's route goes with the bus
func TestOutputLostWithTheBackend(t *testing.T) {
	h := newHarness(t, withOutput(""))
	h.be.AddSpeaker(speakerAddr, speakerName, true)
	h.waitRoute(speakerAddr)
	h.be.Lose()
	h.waitRoute("")
}
