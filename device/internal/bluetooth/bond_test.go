package bluetooth_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth/btfake"
)

type yes struct{}

func (yes) Authorize(bluetooth.AgentRequest) bool { return true }

// the fake as bluez: pairable is mgmt's bondable, and a pairing made without it is gone after a reboot
func TestFakeBondsOnlyWhilePairable(t *testing.T) {
	ctx := context.Background()
	for _, pairable := range []bool{false, true} {
		be := btfake.New()
		if err := be.RegisterAgent(ctx, yes{}); err != nil {
			t.Fatal(err)
		}
		_ = be.SetPairable(ctx, pairable)
		be.Nearby(speakerAddr, speakerName, -50)
		_ = be.StartDiscovery(ctx)
		if err := be.Pair(ctx, btfake.DevicePath(speakerAddr)); err != nil {
			t.Fatal(err)
		}
		be.Reboot()
		snap, _ := be.Snapshot(ctx)
		i := slices.IndexFunc(snap.Devices, func(d bluetooth.Device) bool { return d.Address == speakerAddr })
		if be.Bonded(speakerAddr) != pairable || i < 0 || snap.Devices[i].Paired != pairable {
			t.Fatalf("pairable %t: bonded %t, devices %+v", pairable, be.Bonded(speakerAddr), snap.Devices)
		}
	}
}

func (h *harness) scanAndPair(t *testing.T) {
	t.Helper()
	h.be.Nearby(speakerAddr, speakerName, -50)
	if _, err := h.output("scan", `{}`); err != nil {
		t.Fatal(err)
	}
	if out, err := h.output("pair", `{"id":"`+h.idOf(speakerAddr)+`","sessionId":"wake-1"}`); err != nil ||
		out != `{"paired":true,"connected":true}` {
		t.Fatalf("pair = %s, %v", out, err)
	}
}

func (h *harness) adapter() bluetooth.Adapter {
	h.t.Helper()
	snap, err := h.be.Snapshot(context.Background())
	if err != nil || snap.Adapter == nil {
		h.t.Fatal("no adapter")
	}
	return *snap.Adapter
}

// the bug seen on the pi: an outgoing pair under the lock stored no link key, so the speaker was unpaired after a
// reboot. Now it bonds, the lock is back, and after the reboot the speaker is still paired and comes back by itself.
func TestOutgoingPairBondsAndSurvivesAReboot(t *testing.T) {
	known := t.TempDir() + "/bluetooth/speakers"
	h := newHarness(t, withOutput(known))
	h.waitCall("SetDiscoverable false")
	h.scanAndPair(t)
	if !h.be.Bonded(speakerAddr) {
		t.Fatal("not bonded")
	}
	h.waitRoute(speakerAddr)
	if a := h.adapter(); a.Pairable || a.Discoverable {
		t.Fatalf("the lock is not back: %+v", a)
	}
	h.stop()
	h.be.Reboot()
	n := len(h.be.Calls())

	h2 := newHarness(t, withOutput(known), func(h2 *harness, _ *bluetooth.Options) { h2.be = h.be })
	h2.waitRoute(speakerAddr)
	h2.waitLog(`"msg":"bluetooth_reconnect","id":"` + h.idOf(speakerAddr) + `","attempt":1,"code":"ok"`)
	for _, c := range h2.be.Calls()[n:] {
		if c == "SetPairable true" || c == "SetDiscoverable true" {
			t.Fatalf("a reconnect opened the adapter: %q", h2.be.Calls())
		}
	}
}

// during a window pairable is on already: the pair leaves it alone and the window keeps it
func TestOutgoingPairInsideAWindow(t *testing.T) {
	h := newHarness(t, withOutput(""))
	h.waitCall("SetDiscoverable false")
	if _, err := h.command("pair_window", `{"sessionId":"wake-1"}`); err != nil {
		t.Fatal(err)
	}
	n := len(h.be.Calls())
	h.scanAndPair(t)
	calls := h.be.Calls()[n:]
	if slices.Contains(calls, "SetPairable false") || slices.Contains(calls, "SetPairableTimeout 60") {
		t.Fatalf("the pair touched the window's pairable: %q", calls)
	}
	if !h.be.Bonded(speakerAddr) || !h.adapter().Pairable {
		t.Fatalf("bonded %t, adapter %+v", h.be.Bonded(speakerAddr), h.adapter())
	}
	if s := h.state(); s != nil && s["window"].(map[string]any)["open"] != true {
		t.Fatalf("window closed: %v", s)
	}
}

// a window opened while the pair runs is not closed by its end
func TestWindowOpenedDuringAnOutgoingPair(t *testing.T) {
	h := newHarness(t, withOutput(""))
	h.waitCall("SetDiscoverable false")
	h.be.DuringPair(func() {
		if _, err := h.command("pair_window", `{"sessionId":"wake-1"}`); err != nil {
			t.Error(err)
		}
	})
	h.scanAndPair(t)
	calls := h.be.Calls()
	i := slices.Index(calls, "SetWindowTimeouts 120")
	if i < 0 || slices.Contains(calls[i:], "SetPairable false") || !h.adapter().Pairable || !h.adapter().Discoverable {
		t.Fatalf("the window was locked: %q", calls)
	}
	// its end locks as before
	h.clock.add(121 * time.Second)
	h.be.Update(func(*bluetooth.Snapshot) {})
	waitFor(t, "the lock", func() bool { a := h.adapter(); return !a.Pairable && !a.Discoverable })
}

// pairable is on for a moment outside a window, but our agent still refuses any other device's pairing, and a
// reconcile meanwhile leaves pairable on, so the speaker bonds
func TestIncomingRefusedDuringAnOutgoingPair(t *testing.T) {
	h := newHarness(t, withOutput(""))
	h.waitCall("SetDiscoverable false")
	h.be.DuringPair(func() {
		if !h.adapter().Pairable {
			t.Error("not pairable during the pair")
		}
		if h.be.PairIncoming(phoneAddr, phoneName) {
			t.Error("an incoming pairing accepted during an outgoing pair")
		}
		// a reconcile now must not lock pairable under the pair
		h.be.Update(func(*bluetooth.Snapshot) {})
		time.Sleep(50 * time.Millisecond)
	})
	h.scanAndPair(t)
	h.waitLog(`"msg":"bluetooth_pair_refused","id":"` + h.phoneID() + `","kind":"pairing","reason":"no_window"`)
	if !h.be.Bonded(speakerAddr) {
		t.Fatal("a reconcile locked pairable under the pair")
	}
	if a := h.adapter(); a.Pairable {
		t.Fatalf("the lock is not back: %+v", a)
	}
}

// pairable that cannot be turned on pairs nothing
func TestOutgoingPairNeedsBondable(t *testing.T) {
	h := newHarness(t, withOutput(""))
	h.waitCall("SetDiscoverable false")
	h.be.Nearby(speakerAddr, speakerName, -50)
	if _, err := h.output("scan", `{}`); err != nil {
		t.Fatal(err)
	}
	h.be.Fail("SetPairableTimeout")
	if _, err := h.output("pair", `{"id":"`+h.idOf(speakerAddr)+`","sessionId":"wake-1"}`); code(err) != "unavailable" {
		t.Fatalf("pair: %v", err)
	}
	if slices.Contains(h.be.Calls(), "Pair "+btfake.DevicePath(speakerAddr)) {
		t.Fatal("paired without bondable")
	}
}
