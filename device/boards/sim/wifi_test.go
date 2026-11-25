package sim_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

// the sim's network.wifi over its fake supplicant: a switch lands on the new
// network, the scan names what is around, and a password that never works
// rolls back to the network that held the link (fast here: the board's own
// timings, so only the switch and the scan run in this test)
func TestWiFiCapabilitySwitches(t *testing.T) {
	b, _, _ := newAudiodSim(t)
	if _, ok := b.(board.WiFi); !ok {
		t.Fatal("no wifi profile")
	}
	var w capability.Provider
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID == "network.wifi" {
			w = p
		}
	}
	if w == nil {
		t.Fatal("no network.wifi")
	}
	sa, ok := w.(capability.SecretApplier)
	if !ok {
		t.Fatal("network.wifi takes no secrets")
	}
	ctx := context.Background()
	out, err := w.Command(ctx, "scan", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"ssid":"cadence-sim"`) || !strings.Contains(string(out), `"freqMHz":2412`) {
		t.Fatalf("scan = %s", out)
	}
	// a switch to a network the fake answers: the state lands completed on it
	if err := sa.ApplySecrets(ctx, 1, json.RawMessage(`{"enabled":true,"ssid":"HomeNetB","bssid":"02:00:00:00:00:99"}`),
		map[string]string{"psk": "sim-password-1"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, w, `"ssid":"HomeNetB"`, `"managed":true`)
	// the capability keeps its settings under the data dir
	if err := sa.ApplySecrets(ctx, 2, json.RawMessage(`{"enabled":false}`), nil); err != nil {
		t.Fatal(err)
	}
	waitState(t, w, `"managed":false`, `"ssid":"HomeNetB"`)
}

func waitState(t *testing.T, p capability.Provider, wants ...string) {
	t.Helper()
	deadline := time.After(8 * time.Second)
	var seen string
	for {
		select {
		case s := <-p.State():
			seen = string(s)
			ok := true
			for _, w := range wants {
				if !strings.Contains(seen, w) {
					ok = false
				}
			}
			if ok {
				return
			}
		case <-deadline:
			t.Fatalf("no state with %v (last %s)", wants, seen)
		}
	}
}

// the sim's scan carries no password anywhere
func TestWiFiScanHidesNothing(t *testing.T) {
	b, _, _ := newAudiodSim(t)
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID != "network.wifi" {
			continue
		}
		out, err := p.Command(context.Background(), "scan", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(out)), "password") {
			t.Fatalf("scan = %s", out)
		}
		return
	}
	t.Fatal("no network.wifi")
}

// building the plain sim (no audiod) reports no network.wifi: the fake only
// exists beside the data dir an audiod-mode sim has
func TestPlainSimHasNoWiFi(t *testing.T) {
	plain := sim.New(func(string) string { return "1" }, t.TempDir(), nil)
	if p := plain.(board.WiFi).WiFi(); p.Interface != "" || p.Dial != nil {
		t.Fatalf("plain sim answers wifi: %+v", p)
	}
	for _, p := range plain.Capabilities() {
		if p.Descriptor().ID == "network.wifi" {
			t.Fatal("plain sim reports network.wifi")
		}
	}
}

// the sim's no-server network: it associates with an address, and the link's
// gate stays shut while it holds the link; selecting the old network opens it
func TestWiFiNoServerNetworkShutsTheLink(t *testing.T) {
	b, _, _ := newAudiodSim(t)
	gate, ok := b.(board.LinkGate)
	if !ok {
		t.Fatal("no link gate")
	}
	be, err := b.(board.WiFi).WiFi().Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if gate.LinkBlocked() {
		t.Fatal("blocked at boot")
	}
	id, err := be.Upsert(ctx, "Captive", "no-server-route", "", 3000)
	if err != nil {
		t.Fatal(err)
	}
	if err := be.Select(ctx, id); err != nil {
		t.Fatal(err)
	}
	if st, _ := be.Status(ctx); st.SSID != "Captive" || st.IP == "" || !gate.LinkBlocked() {
		t.Fatalf("status %+v blocked %v", st, gate.LinkBlocked())
	}
	if err := be.Disable(ctx, id); err != nil {
		t.Fatal(err)
	}
	saved, _ := be.List(ctx)
	if len(saved) != 2 || !saved[id].Disabled {
		t.Fatalf("saved = %+v", saved)
	}
	if err := be.Select(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if gate.LinkBlocked() {
		t.Fatal("still blocked on the old network")
	}
}
