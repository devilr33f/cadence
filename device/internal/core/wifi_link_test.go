package core_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	"git.shino.boo/devilreef/cadence/device/internal/link"
	"git.shino.boo/devilreef/cadence/device/internal/wifi"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// airwaves is a fake supplicant beside a fake route to the server: a network
// whose psk is "no-server-route" associates, but blocks every dial of the link
type airwaves struct {
	mu    sync.Mutex
	nets  []wifi.Saved
	psks  map[int]string
	st    wifi.Status
	sels  []int
	block bool
}

func newAirwaves() *airwaves {
	return &airwaves{
		nets: []wifi.Saved{{ID: 0, SSID: "Home", BSSID: "any"}},
		psks: map[int]string{0: "home-password"},
		st:   wifi.Status{State: wifi.StateCompleted, SSID: "Home", IP: "192.0.2.10", CurrentID: 0},
	}
}

func (a *airwaves) Status(context.Context) (wifi.Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.st, nil
}

func (a *airwaves) List(context.Context) ([]wifi.Saved, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]wifi.Saved(nil), a.nets...), nil
}

func (a *airwaves) Upsert(_ context.Context, ssid, psk, _ string, _ int) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, n := range a.nets {
		if n.SSID == ssid {
			a.psks[n.ID] = psk
			return n.ID, nil
		}
	}
	id := len(a.nets)
	a.nets = append(a.nets, wifi.Saved{ID: id, SSID: ssid, BSSID: "any"})
	a.psks[id] = psk
	return id, nil
}

func (a *airwaves) Select(_ context.Context, id int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sels = append(a.sels, id)
	a.st = wifi.Status{State: wifi.StateCompleted, SSID: a.nets[id].SSID, IP: "192.0.2.10", CurrentID: id}
	a.block = a.psks[id] == "no-server-route"
	return nil
}

func (a *airwaves) Disable(context.Context, int) error { return nil }
func (a *airwaves) Scan(context.Context) ([]wifi.AP, error) {
	return nil, nil
}

func (a *airwaves) blocked() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.block
}

// startWiFiSync links a board with network.wifi on air, with the link's redial
// and gate wired as core's Run wires them.
func startWiFiSync(t *testing.T, air *airwaves, linkWithin time.Duration) (*linkServer, *syncBuf) {
	t.Helper()
	ls, buf := newLinkServer(t), &syncBuf{}
	log := newLog(buf)
	ctx, cancel := context.WithCancel(context.Background())
	w := wifi.NewCapabilityWith(ctx, func(context.Context) (wifi.Backend, error) { return air, nil },
		wifi.SettingsPath(t.TempDir()), log,
		wifi.Timings{IPWithin: 2 * time.Second, LinkWithin: linkWithin, Settle: time.Hour, Poll: 10 * time.Millisecond})
	b := fakeBoard{id: "speaker", caps: []capability.Provider{w}}
	s, err := core.NewSync(b, openStore(t), "v-test", log)
	if err != nil {
		t.Fatal(err)
	}
	wait := s.StartAudio(ctx)
	done := make(chan error, 1)
	go func() {
		done <- link.Run(ctx, link.Dialer{URL: ls.url, Token: token, Redial: s.Redials(), Blocked: air.blocked}, s, log)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		wait()
		w.Stop()
	})
	return ls, buf
}

// connectWiFi pushes a switch with its password. its ack may be lost with the
// old link, as over a real switch: the next hello's push of the same
// generation is acked from the store
func connectWiFi(p *peer, ssid, psk string) {
	p.t.Helper()
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_ConfigSet{ConfigSet: &cadencev2.ConfigSet{
		Capability: wifi.CapabilityID, Generation: 1, Json: `{"enabled":true,"ssid":"` + ssid + `"}`,
		Secrets: map[string]string{"psk": psk},
	}}})
}

// wifiState reads the link until a network.wifi state holding want.
func wifiState(p *peer, want string) string {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d := p.recv().GetStateDelta(); d != nil && d.GetCapability() == wifi.CapabilityID && strings.Contains(d.GetJson(), want) {
			return d.GetJson()
		}
	}
	p.t.Fatalf("no wifi state with %s", want)
	return ""
}

// a switch to a network that reaches the server finishes once the link came
// back over it: the address made the link redial at once (the old link's close
// waits up to 1 s for an answer the test never sends)
func TestWiFiSwitchFinishesOverTheNewLink(t *testing.T) {
	air := newAirwaves()
	ls, buf := startWiFiSync(t, air, 4*time.Second)
	p := ls.accept(t)
	p.handshake(false)
	waitLog(t, buf, "link_up")
	connectWiFi(p, "Upstairs", "upstairs-password")
	// the old link goes: the network under it changed
	second := ls.accept(t)
	second.handshake(false)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(buf.String())
		}
	})
	got := wifiState(second, `"ssid":"Upstairs"`)
	for strings.Contains(got, "switching") {
		got = wifiState(second, `"ssid":"Upstairs"`)
	}
	if strings.Contains(got, `"code"`) {
		t.Fatalf("state = %s", got)
	}
	if !strings.Contains(buf.String(), `"msg":"wifi_switched"`) || strings.Contains(buf.String(), "upstairs-password") {
		t.Fatalf("log: %s", buf.String())
	}
}

// a network that associates but cannot reach the server is rolled back with
// no_server, and the device links again over the previous one
func TestWiFiSwitchWithoutServerRollsBackAndRelinks(t *testing.T) {
	air := newAirwaves()
	ls, buf := startWiFiSync(t, air, 400*time.Millisecond)
	p := ls.accept(t)
	p.handshake(false)
	waitLog(t, buf, "link_up")
	connectWiFi(p, "Captive", "no-server-route")
	// no dial gets through until the rollback; then the device is back
	second := ls.accept(t)
	second.handshake(false)
	got := wifiState(second, `"code":"rolled_back"`)
	if !strings.Contains(got, `"reason":"no_server"`) || !strings.Contains(got, `"ssid":"Home"`) {
		t.Fatalf("state = %s", got)
	}
	air.mu.Lock()
	sels := append([]int(nil), air.sels...)
	air.mu.Unlock()
	if len(sels) != 2 || sels[0] != 1 || sels[1] != 0 {
		t.Fatalf("selects = %v", sels)
	}
	if strings.Contains(buf.String(), "no-server-route") {
		t.Fatalf("the log has the psk: %s", buf.String())
	}
}
