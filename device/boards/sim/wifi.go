package sim

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/wifi"
)

// simSSID is the network the fake holds at boot, with simIP.
const (
	simSSID = "cadence-sim"
	simIP   = "192.0.2.10"
	// simFailPSK makes a selected network never associate (a rollback with
	// no_ip); not a password anywhere
	simFailPSK = "wrong-password"
	// simNoServerPSK makes a selected network associate with an address from
	// which the server cannot be reached (a rollback with no_server): the
	// sim's link refuses to dial while it holds the link (board.LinkGate)
	simNoServerPSK = "no-server-route"
)

var errUnknownNetwork = errors.New("sim: unknown network")

// wifiSim is the sim's wi-fi: an in-memory supplicant, no radio. it starts
// associated to cadence-sim; an upsert adds (never removes) and a select
// associates at once, unless the network's psk is simFailPSK, which leaves it
// disassociated forever, or simNoServerPSK, which associates with an address
// but blocks the link to the server while it holds the link. For tests only.
type wifiSim struct {
	mu      sync.Mutex
	saved   []wifiSimNet
	current int
	st      wifi.Status
}

type wifiSimNet struct {
	id       int
	ssid     string
	bssid    string
	psk      string
	priority int
	disabled bool
}

func newWiFiSim() *wifiSim {
	return &wifiSim{
		saved:   []wifiSimNet{{id: 0, ssid: simSSID, bssid: "any", psk: "cadence-sim-psk", priority: 2222}},
		current: 0,
		st: wifi.Status{State: wifi.StateCompleted, SSID: simSSID, BSSID: "02:00:00:00:00:01",
			FreqMHz: 5180, IP: simIP, CurrentID: 0},
	}
}

func (s *wifiSim) Status(context.Context) (wifi.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st, nil
}

func (s *wifiSim) List(context.Context) ([]wifi.Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]wifi.Saved, 0, len(s.saved))
	for _, n := range s.saved {
		out = append(out, wifi.Saved{ID: n.id, SSID: n.ssid, BSSID: n.bssid, Disabled: n.disabled})
	}
	return out, nil
}

func (s *wifiSim) Upsert(_ context.Context, ssid, psk, bssid string, priority int) (int, error) {
	if bssid == "" {
		bssid = "any"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.saved {
		if n.ssid == ssid && n.bssid == bssid {
			s.saved[i].psk, s.saved[i].priority, s.saved[i].disabled = psk, priority, false
			return n.id, nil
		}
	}
	id := len(s.saved)
	s.saved = append(s.saved, wifiSimNet{id: id, ssid: ssid, bssid: bssid, psk: psk, priority: priority})
	return id, nil
}

func (s *wifiSim) Select(_ context.Context, id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.saved {
		if n.id != id {
			continue
		}
		s.saved[i].disabled = false
		s.current = id
		if n.psk == simFailPSK {
			s.st = wifi.Status{State: wifi.StateDisconnected, CurrentID: -1}
			return nil
		}
		bssid := n.bssid
		if bssid == "any" {
			bssid = fmt.Sprintf("02:00:00:00:00:%02d", id+1)
		}
		s.st = wifi.Status{State: wifi.StateCompleted, SSID: n.ssid, BSSID: bssid,
			FreqMHz: 5180, IP: simIP, CurrentID: id}
		return nil
	}
	return errUnknownNetwork
}

func (s *wifiSim) Disable(_ context.Context, id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.saved {
		if n.id == id {
			s.saved[i].disabled = true
			return nil
		}
	}
	return errUnknownNetwork
}

// serverBlocked says whether the network holding the link cannot reach the
// server (its psk is simNoServerPSK).
func (s *wifiSim) serverBlocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.State != wifi.StateCompleted {
		return false
	}
	for _, n := range s.saved {
		if n.id == s.st.CurrentID {
			return n.psk == simNoServerPSK
		}
	}
	return false
}

func (s *wifiSim) Scan(context.Context) ([]wifi.AP, error) {
	return []wifi.AP{
		{SSID: simSSID, BSSID: "02:00:00:00:00:01", FreqMHz: 5180, SignalDbm: -40, Security: "[WPA2-PSK-CCMP][ESS]"},
		{SSID: "cadence-2.4", BSSID: "02:00:00:00:00:02", FreqMHz: 2412, SignalDbm: -52, Security: "[WPA2-PSK-CCMP][ESS]"},
		{SSID: "", BSSID: "02:00:00:00:00:03", FreqMHz: 2437, SignalDbm: -61, Security: "[WPA2-PSK-CCMP][ESS]"},
	}, nil
}

// WiFi is the sim's fake supplicant (board.WiFi), empty without one.
func (s *sim) WiFi() board.WiFiProfile {
	if s.wifiSim == nil {
		return board.WiFiProfile{}
	}
	return board.WiFiProfile{Interface: "wlan0",
		Dial: func(context.Context) (wifi.Backend, error) { return s.wifiSim, nil }}
}

// LinkBlocked fakes a network with no route to the server: the link's dials
// fail while the fake supplicant holds one whose psk is simNoServerPSK.
func (s *sim) LinkBlocked() bool { return s.wifiSim != nil && s.wifiSim.serverBlocked() }

var _ board.WiFi = (*sim)(nil)
var _ board.LinkGate = (*sim)(nil)
