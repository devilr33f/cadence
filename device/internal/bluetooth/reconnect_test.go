package bluetooth_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth/btfake"
)

// rebooted: a bonded, trusted speaker known here, disconnected, the adapter unpowered; quick backoffs
func rebooted(t *testing.T, first, most, giveUp time.Duration, prepare func(be *btfake.Backend)) func(h *harness, o *bluetooth.Options) {
	t.Helper()
	known := filepath.Join(t.TempDir(), "speakers")
	if err := os.WriteFile(known, []byte(bluetooth.ShortID("00:11:22:33:44:55", speakerAddr)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(h *harness, o *bluetooth.Options) {
		withOutput(known)(h, o)
		o.Output.ReconnectFirst, o.Output.ReconnectMax, o.Output.ReconnectFor = first, most, giveUp
		h.be.AddSpeaker(speakerAddr, speakerName, false)
		h.be.Reboot()
		if prepare != nil {
			prepare(h.be)
		}
	}
}

func (h *harness) connects() int {
	return len(slices.DeleteFunc(h.be.Calls(), func(c string) bool { return !strings.HasPrefix(c, "Connect ") }))
}

func (h *harness) reconnectLog(attempt int, code string) string {
	return `"msg":"bluetooth_reconnect","id":"` + h.idOf(speakerAddr) + `","attempt":` + string(rune('0'+attempt)) +
		`,"code":"` + code + `"`
}

// at start the known speaker is connected once the adapter is powered: no pairing, nothing discoverable
func TestReconnectAtStart(t *testing.T) {
	h := newHarness(t, rebooted(t, time.Millisecond, time.Millisecond, time.Second, nil))
	h.waitRoute(speakerAddr)
	h.waitLog(h.reconnectLog(1, "ok"))
	calls := h.be.Calls()
	if i, j := slices.Index(calls, "SetPowered true"), slices.Index(calls, "Connect "+btfake.DevicePath(speakerAddr)); i < 0 || j < i {
		t.Fatalf("calls %q", calls)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "Pair") || c == "SetPairable true" || c == "SetDiscoverable true" {
			t.Fatalf("a reconnect paired or opened the adapter: %q", calls)
		}
	}
	// the owner's disconnect is left alone
	if _, err := h.output("disconnect", `{"id":"`+h.idOf(speakerAddr)+`","sessionId":"wake-1"}`); err != nil {
		t.Fatal(err)
	}
	h.waitRoute("")
	time.Sleep(50 * time.Millisecond)
	if n := h.connects(); n != 1 {
		t.Fatalf("%d connects after the owner's disconnect", n)
	}
}

// failures back off and the first success ends the attempts
func TestReconnectBacksOffUntilItConnects(t *testing.T) {
	h := newHarness(t, rebooted(t, 5*time.Millisecond, 20*time.Millisecond, time.Minute, func(be *btfake.Backend) {
		be.FailNext("Connect", 3)
	}))
	h.waitLog(h.reconnectLog(1, "connect_failed"))
	h.waitLog(h.reconnectLog(4, "ok"))
	h.waitRoute(speakerAddr)
	time.Sleep(60 * time.Millisecond)
	if n := h.connects(); n != 4 {
		t.Fatalf("%d connects", n)
	}
}

// it gives up, then tries again when the adapter comes back, and when discovery sees the speaker again
func TestReconnectGivesUpAndTriesAgain(t *testing.T) {
	h := newHarness(t, rebooted(t, 2*time.Millisecond, 4*time.Millisecond, 10*time.Millisecond, func(be *btfake.Backend) {
		be.FailNext("Connect", 1000)
	}))
	// waits 2, 4, 4: the next 4 passes 10
	h.waitLog(`"msg":"bluetooth_reconnect","id":"` + h.idOf(speakerAddr) + `","attempt":4,"code":"gave_up"`)
	time.Sleep(30 * time.Millisecond)
	if n := h.connects(); n != 4 {
		t.Fatalf("%d connects after giving up", n)
	}

	// in range again: one attempt now, still failing, then the backoff again
	h.be.Update(func(s *bluetooth.Snapshot) {
		for i := range s.Devices {
			s.Devices[i].RSSI = -60
		}
	})
	waitFor(t, "an attempt on rssi", func() bool { return h.connects() > 4 })
	waitFor(t, "a second give-up", func() bool {
		return strings.Count(h.logs.String(), `"code":"gave_up"`) == 2
	})

	// the adapter gone and back
	h.be.FailNext("Connect", 0)
	var saved *bluetooth.Adapter
	h.be.Update(func(s *bluetooth.Snapshot) { saved, s.Adapter = s.Adapter, nil })
	h.waitLog(`"msg":"bluetooth_adapter_gone"`)
	h.be.Update(func(s *bluetooth.Snapshot) { s.Adapter = saved })
	h.waitLog(h.reconnectLog(1, "ok"))
	h.waitRoute(speakerAddr)
}

// only a bonded, trusted speaker comes back
func TestReconnectSkipsAnUntrustedSpeaker(t *testing.T) {
	h := newHarness(t, rebooted(t, time.Millisecond, time.Millisecond, time.Second, func(be *btfake.Backend) {
		be.Update(func(s *bluetooth.Snapshot) { s.Devices[0].Trusted = false })
	}))
	h.waitCall("SetPowered true")
	time.Sleep(50 * time.Millisecond)
	if n := h.connects(); n != 0 {
		t.Fatalf("%d connects to an untrusted speaker", n)
	}
}

// of two known speakers, the most recently active one comes back
func TestReconnectTheMostRecentSpeaker(t *testing.T) {
	known := filepath.Join(t.TempDir(), "speakers")
	ids := bluetooth.ShortID("00:11:22:33:44:55", speakerAddr) + "\n" + bluetooth.ShortID("00:11:22:33:44:55", farAddr) + "\n"
	if err := os.WriteFile(known, []byte(ids), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(h *harness, o *bluetooth.Options) {
		withOutput(known)(h, o)
		h.be.AddSpeaker(speakerAddr, speakerName, false)
		h.be.AddSpeaker(farAddr, "Far", false)
		h.be.Reboot()
	})
	h.waitRoute(farAddr)
	if n := h.connects(); n != 1 || !slices.Contains(h.be.Calls(), "Connect "+btfake.DevicePath(farAddr)) {
		t.Fatalf("calls %q", h.be.Calls())
	}
}
