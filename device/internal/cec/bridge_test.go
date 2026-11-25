package cec_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/cec"
)

// fakeApp is the app's side of the cec bridge for tests: it answers the ops and keeps what it was asked
type fakeApp struct {
	ln      net.Listener
	mu      sync.Mutex
	asked   []string
	info    string
	screen  string
	volume  string
	closing chan struct{}
}

func startFakeApp(t *testing.T, info string) *fakeApp {
	t.Helper()
	var own struct {
		Shell       bool `json:"shell"`
		FixedVolume bool `json:"fixedVolume"`
	}
	_ = json.Unmarshal([]byte(info), &own)
	// the ops' ok follows what the app can do: the shell keys only paired, the volume keys only through
	// the framework's forward (a fixed stream volume)
	app := &fakeApp{info: info,
		screen:  fmt.Sprintf(`{"ok":%t}`, own.Shell),
		volume:  fmt.Sprintf(`{"ok":%t}`, own.FixedVolume),
		closing: make(chan struct{})}
	dir, err := os.MkdirTemp("", "cadence-cec-bridge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	app.ln, err = net.Listen("unix", filepath.Join(dir, "cec.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ln.Close() })
	go app.serve()
	return app
}

func (a *fakeApp) serve() {
	for {
		c, err := a.ln.Accept()
		if err != nil {
			return
		}
		r := bufio.NewReader(c)
		line, err := r.ReadString('\n')
		if err != nil {
			_ = c.Close()
			continue
		}
		var req struct {
			Op     string `json:"op"`
			On     bool   `json:"on"`
			Action string `json:"action"`
		}
		_ = json.Unmarshal([]byte(line), &req)
		a.mu.Lock()
		a.asked = append(a.asked, strings.TrimSpace(line))
		answer := a.info
		switch req.Op {
		case "screen":
			answer = a.screen
		case "volume":
			answer = a.volume
		}
		a.mu.Unlock()
		_, _ = c.Write([]byte(answer + "\n"))
		_ = c.Close()
	}
}

func (a *fakeApp) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.asked...)
}

// overBridge is a controller on the app's bridge, already started and scanned
func overBridge(t *testing.T, info string) (*cec.Controller, *fakeApp) {
	t.Helper()
	app := startFakeApp(t, info)
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); wg.Wait() })
	bridge := cec.NewBridge(app.ln.Addr().String(), nil)
	bridge.Start(ctx, &wg)
	c := cec.New(bridge, nil)
	c.Scan(context.Background())
	return c, app
}

func TestTheAppBridgeAnswersTheScan(t *testing.T) {
	c, _ := overBridge(t, `{"present":true,"fixedVolume":true,"shell":true,"tvPower":"on"}`)
	s := state(t, c)
	// the tv is the wire's root, with what the framework knows and nothing it does not
	for _, want := range []string{`"present":true`, `"type":"tv"`, `"physicalAddress":"0.0.0.0"`, `"power":"on"`,
		`"systemAudio":{"available":false,"enabled":"unknown"}`, `"volumeTarget":"tv"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("state %s misses %s", s, want)
		}
	}
	if strings.Contains(s, "vendorId") || strings.Contains(s, "audioStatus") {
		t.Fatalf("state invents what the app cannot know: %s", s)
	}
}

func TestTheAppBridgeCarriesTheFlows(t *testing.T) {
	c, app := overBridge(t, `{"present":true,"fixedVolume":true,"shell":true,"tvPower":"on"}`)
	if err := command(t, c, "power", `{"action":"off"}`); err != nil {
		t.Fatal(err)
	}
	if err := command(t, c, "power", `{"action":"toggle"}`); err != nil {
		t.Fatal(err)
	}
	if err := command(t, c, "volume", `{"action":"up","steps":2}`); err != nil {
		t.Fatal(err)
	}
	if err := command(t, c, "volume", `{"action":"mute"}`); err != nil {
		t.Fatal(err)
	}
	// one touch play is the home key once (the pair of view ons the controller sends both carry it), a
	// standby the sleep key, a volume press one adjust each
	var screens, volumes int
	for _, r := range app.requests() {
		switch {
		case strings.Contains(r, `"op":"screen"`) && strings.Contains(r, `"on":false`):
			screens++
		case strings.Contains(r, `"op":"volume"`):
			volumes++
		}
	}
	if screens != 1 {
		t.Fatalf("%d standby ops, want 1: %v", screens, app.requests())
	}
	if volumes != 3 {
		t.Fatalf("%d volume ops, want 3: %v", volumes, app.requests())
	}
}

func TestTheAppBridgeWithoutCecIsAbsent(t *testing.T) {
	c, _ := overBridge(t, `{"present":false,"fixedVolume":false,"shell":false,"tvPower":"unknown"}`)
	s := state(t, c)
	if !strings.Contains(s, `"present":false`) || !strings.Contains(s, `"devices":[]`) {
		t.Fatalf("state %s", s)
	}
	if _, err := c.Command(context.Background(), "power", []byte(`{"action":"on"}`)); err == nil || err.Error() != "command failed: no_bus" {
		t.Fatalf("err = %v", err)
	}
}

func TestTheAppBridgeRefusesWhatItCannotDo(t *testing.T) {
	// no shell pairing: the power keys cannot run, and the flow says so
	c, _ := overBridge(t, `{"present":true,"fixedVolume":false,"shell":false,"tvPower":"on"}`)
	if err := command(t, c, "power", `{"action":"off"}`); err == nil || err.Error() != "command failed: no_target" {
		t.Fatalf("err = %v", err)
	}
}
