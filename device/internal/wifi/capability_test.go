package wifi_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/wifi"
)

// fakeBackend scripts a supplicant: it records what it was asked and answers
// from fields the test moves between calls.
type fakeBackend struct {
	mu       sync.Mutex
	upserts  []string
	selects  []int
	disables []int
	status   wifi.Status
	list     []wifi.Saved
	scan     []wifi.AP
	fail     bool
}

func (f *fakeBackend) Status(context.Context) (wifi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return wifi.Status{}, errors.New("backend down")
	}
	return f.status, nil
}

func (f *fakeBackend) List(context.Context) ([]wifi.Saved, error) { return f.list, nil }

func (f *fakeBackend) Upsert(_ context.Context, ssid, psk, bssid string, priority int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, ssid+"|"+psk+"|"+bssid+"|"+strconv.Itoa(priority))
	for _, n := range f.list {
		if n.SSID == ssid {
			return n.ID, nil
		}
	}
	return len(f.list), nil
}

func (f *fakeBackend) Select(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selects = append(f.selects, id)
	return nil
}

func (f *fakeBackend) Disable(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disables = append(f.disables, id)
	return nil
}

func (f *fakeBackend) Scan(context.Context) ([]wifi.AP, error) { return f.scan, nil }

func (f *fakeBackend) disabled() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int{}, f.disables...)
}

func (f *fakeBackend) calls() (ups []string, sels []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.upserts...), append([]int{}, f.selects...)
}

// testTimings: 60 ms for the address, 60 ms more for the link
var testTimings = wifi.Timings{IPWithin: 60 * time.Millisecond, LinkWithin: 60 * time.Millisecond,
	Settle: 20 * time.Millisecond, Poll: 5 * time.Millisecond}

func newCap(t *testing.T, be wifi.Backend) (*wifi.Capability, *fakeBackend, string) {
	t.Helper()
	if be == nil {
		be = &fakeBackend{}
	}
	f := be.(*fakeBackend) //nolint:forcetypeassert // the tests' own type
	dir := t.TempDir()
	return openCap(t, f, dir), f, dir
}

// openCap starts a capability on dir's settings, as a boot does.
func openCap(t *testing.T, f *fakeBackend, dir string) *wifi.Capability {
	t.Helper()
	c := wifi.NewCapabilityWith(context.Background(),
		func(context.Context) (wifi.Backend, error) { return f, nil },
		wifi.SettingsPath(dir), slog.New(slog.DiscardHandler), testTimings)
	t.Cleanup(c.Stop)
	return c
}

// redials counts the capability's requests to dial again.
type redials struct{ n atomic.Int32 }

func watchRedials(c *wifi.Capability) *redials {
	r := &redials{}
	c.SetRedial(func() { r.n.Add(1) })
	return r
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func applyErr(t *testing.T, c *wifi.Capability, cfg string, secrets map[string]string) error {
	t.Helper()
	err := c.ApplySecrets(context.Background(), 1, json.RawMessage(cfg), secrets)
	var ae *capability.ApplyError
	if err != nil && !errors.As(err, &ae) {
		t.Fatalf("not an apply error: %v", err)
	}
	return err
}

func drain(t *testing.T, c *wifi.Capability) []string {
	t.Helper()
	var out []string
	for {
		select {
		case s := <-c.State():
			out = append(out, string(s))
		default: //nolint:staticcheck // the empty default is the drain stop
			if len(out) == 0 {
				t.Fatal("no state offered")
			}
			return out
		}
	}
}

func waitFor(t *testing.T, c *wifi.Capability, code string) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case s := <-c.State():
			if strings.Contains(string(s), code) {
				return string(s)
			}
		case <-deadline:
			t.Fatalf("no state with %q", code)
		}
	}
}

func setLink(f *fakeBackend, st wifi.Status) { f.mu.Lock(); f.status = st; f.mu.Unlock() }

func TestApplyInvalidConfigs(t *testing.T) {
	c, f, _ := newCap(t, nil)
	for _, cfg := range []string{
		`{"ssid":"x"}`,     // no enabled
		`{"enabled":true}`, // enabled without a ssid
		`{"enabled":true,"ssid":"","psk":"12345678"}`,  // empty ssid
		`{"enabled":true,"ssid":"x","bssid":"no-mac"}`, // bad bssid
		`{"enabled":true,"ssid":"x","priority":10000}`, // priority out of range
		`{"enabled":true,"ssid":"x","other":1}`,        // unknown field
		`{"enabled":"yes"}`,                            // wrong type
		`null`,                                         // not an object
	} {
		var ae *capability.ApplyError
		if err := c.ApplySecrets(context.Background(), 1, json.RawMessage(cfg), nil); !errors.As(err, &ae) || ae.Code != "invalid" {
			t.Fatalf("cfg %s: err = %v, want invalid", cfg, err)
		}
	}
	if ups, _ := f.calls(); len(ups) != 0 {
		t.Fatalf("touched the supplicant: %v", ups)
	}
}

func TestDisabledNeverTouchesTheSupplicant(t *testing.T) {
	c, f, _ := newCap(t, nil)
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 0})
	// the watcher's first poll reports the link, unmanaged
	if got := waitFor(t, c, `"state":"completed"`); !strings.Contains(got, `"managed":false`) {
		t.Fatalf("state = %s", got)
	}
	if err := applyErr(t, c, `{"enabled":false}`, nil); err != nil {
		t.Fatal(err)
	}
	if ups, sels := f.calls(); len(ups) != 0 || len(sels) != 0 {
		t.Fatalf("touched: %v %v", ups, sels)
	}
}

func TestMissingPskReportsAndStaysPut(t *testing.T) {
	c, f, _ := newCap(t, nil)
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 0})
	if err := applyErr(t, c, `{"enabled":true,"ssid":"HomeNetB"}`, map[string]string{"psk": "short"}); err != nil {
		t.Fatal(err)
	}
	if got := waitFor(t, c, "psk_missing"); !strings.Contains(got, `"managed":true`) {
		t.Fatalf("state = %s", got)
	}
	if ups, _ := f.calls(); len(ups) != 0 {
		t.Fatalf("touched: %v", ups)
	}
}

func TestSwitchSelectsAndSettlesOnceLinked(t *testing.T) {
	c, f, dir := newCap(t, nil)
	r := watchRedials(c)
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 0})
	if err := applyErr(t, c, `{"enabled":true,"ssid":"HomeNetB","bssid":"02:00:00:00:00:a3","priority":3333}`,
		map[string]string{"psk": "Bedroom123#"}); err != nil {
		t.Fatal(err)
	}
	ups, sels := f.calls()
	if len(ups) != 1 || !strings.HasPrefix(ups[0], "HomeNetB|") {
		t.Fatalf("upserts = %v", ups)
	}
	if len(sels) != 1 || sels[0] != 0 {
		t.Fatalf("selects = %v", sels)
	}
	// a link that was up over the old network counts for nothing
	c.LinkChanged(true)
	waitFor(t, c, `"code":"switching"`)
	// the address comes: the link is told to dial again over it
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNetB", BSSID: "02:00:00:00:00:a3",
		FreqMHz: 5180, IP: "192.168.1.182", CurrentID: 1})
	eventually(t, "a redial at the address", func() bool { return r.n.Load() == 1 })
	if got := settingsOf(t, dir); !strings.Contains(got, `"pending"`) {
		t.Fatalf("no pending switch kept: %s", got)
	}
	c.LinkChanged(false)
	c.LinkChanged(true)
	got := waitFor(t, c, `"ssid":"HomeNetB","bssid"`)
	for strings.Contains(got, "switching") {
		got = waitFor(t, c, `"ssid":"HomeNetB","bssid"`)
	}
	if strings.Contains(got, "code") || !strings.Contains(got, `"target":"HomeNetB"`) {
		t.Fatalf("state = %s", got)
	}
	eventually(t, "the pending switch cleared", func() bool { return !strings.Contains(settingsOf(t, dir), "pending") })
	// an idempotent re-apply selects nothing
	if err := applyErr(t, c, `{"enabled":true,"ssid":"HomeNetB","bssid":"02:00:00:00:00:a3"}`,
		map[string]string{"psk": "Bedroom123#"}); err != nil {
		t.Fatal(err)
	}
	if _, sels := f.calls(); len(sels) != 1 {
		t.Fatalf("selects after re-apply = %v", sels)
	}
	if d := f.disabled(); len(d) != 0 {
		t.Fatalf("disabled = %v", d)
	}
}

func TestSwitchWithoutAddressRollsBackNoIP(t *testing.T) {
	c, f, _ := newCap(t, &fakeBackend{list: []wifi.Saved{{ID: 4, SSID: "HomeNet", BSSID: "any"}}})
	r := watchRedials(c)
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 4})
	if err := applyErr(t, c, `{"enabled":true,"ssid":"HomeNetB"}`, map[string]string{"psk": "wrong-password"}); err != nil {
		t.Fatal(err)
	}
	setLink(f, wifi.Status{State: wifi.StateDisconnected, CurrentID: -1})
	got := waitFor(t, c, "rolled_back")
	if !strings.Contains(got, `"managed":true`) || !strings.Contains(got, `"reason":"no_ip"`) {
		t.Fatalf("state = %s", got)
	}
	if _, sels := f.calls(); len(sels) != 2 || sels[1] != 4 {
		t.Fatalf("selects = %v, want the previous id back", sels)
	}
	// the target stays saved, disabled
	if d := f.disabled(); len(d) != 1 || d[0] != 1 {
		t.Fatalf("disabled = %v", d)
	}
	if r.n.Load() != 1 {
		t.Fatalf("redials = %d, want one at the rollback", r.n.Load())
	}
	// back on the previous network with no link: one more redial, at once
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 4})
	eventually(t, "a redial on rejoining", func() bool { return r.n.Load() == 2 })
}

func TestSwitchWithoutServerRollsBackNoServer(t *testing.T) {
	c, f, dir := newCap(t, &fakeBackend{list: []wifi.Saved{{ID: 0, SSID: "HomeNet", BSSID: "any"}}})
	r := watchRedials(c)
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 0})
	c.LinkChanged(true)
	if err := applyErr(t, c, `{"enabled":true,"ssid":"Captive"}`, map[string]string{"psk": "no-server-route"}); err != nil {
		t.Fatal(err)
	}
	c.LinkChanged(false)
	// it associates with an address, but the link never comes back
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "Captive", IP: "10.0.0.5", CurrentID: 1})
	got := waitFor(t, c, "rolled_back")
	if !strings.Contains(got, `"reason":"no_server"`) {
		t.Fatalf("state = %s", got)
	}
	if _, sels := f.calls(); len(sels) != 2 || sels[1] != 0 {
		t.Fatalf("selects = %v", sels)
	}
	if d := f.disabled(); len(d) != 1 || d[0] != 1 {
		t.Fatalf("disabled = %v", d)
	}
	// one redial at the address, one at the rollback
	if n := r.n.Load(); n != 2 {
		t.Fatalf("redials = %d", n)
	}
	kept := settingsOf(t, dir)
	if strings.Contains(kept, "pending") || !strings.Contains(kept, `"rolledBackReason":"no_server"`) {
		t.Fatalf("settings = %s", kept)
	}
	// the device re-links over the old network: the state keeps the rollback
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 0})
	c.LinkChanged(true)
	got = waitFor(t, c, `"ssid":"HomeNet",`)
	if !strings.Contains(got, `"code":"rolled_back","reason":"no_server"`) {
		t.Fatalf("state = %s", got)
	}
}

func TestRollbackHoldsUntilANewerGeneration(t *testing.T) {
	c, f, _ := newCap(t, nil)
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 4})
	if err := c.ApplySecrets(context.Background(), 7, json.RawMessage(`{"enabled":true,"ssid":"HomeNetB"}`),
		map[string]string{"psk": "wrong-password"}); err != nil {
		t.Fatal(err)
	}
	setLink(f, wifi.Status{State: wifi.StateDisconnected, CurrentID: -1})
	waitFor(t, c, "rolled_back")
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 4})
	// the same generation again (a boot's restore) stays put
	if err := c.ApplySecrets(context.Background(), 7, json.RawMessage(`{"enabled":true,"ssid":"HomeNetB"}`),
		map[string]string{"psk": "wrong-password"}); err != nil {
		t.Fatal(err)
	}
	if ups, sels := f.calls(); len(ups) != 1 || len(sels) != 2 {
		t.Fatalf("after a restore: upserts %v selects %v", ups, sels)
	}
	// a newer push naming it (the dashboard's retry) tries again
	if err := c.ApplySecrets(context.Background(), 8, json.RawMessage(`{"enabled":true,"ssid":"HomeNetB"}`),
		map[string]string{"psk": "right-password"}); err != nil {
		t.Fatal(err)
	}
	if ups, sels := f.calls(); len(ups) != 2 || len(sels) != 3 {
		t.Fatalf("retry: upserts %v selects %v", ups, sels)
	}
	waitFor(t, c, `"code":"switching"`)
}

func TestLegacyRollbackHoldsAtRestore(t *testing.T) {
	f := &fakeBackend{status: wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 0}}
	dir := t.TempDir()
	writeSettings(t, dir, `{"enabled":true,"ssid":"HomeNetB","rolledBack":"HomeNetB"}`)
	c := openCap(t, f, dir)
	if err := c.ApplySecrets(context.Background(), 3, json.RawMessage(`{"enabled":true,"ssid":"HomeNetB"}`),
		map[string]string{"psk": "Bedroom123#"}); err != nil {
		t.Fatal(err)
	}
	if ups, _ := f.calls(); len(ups) != 0 {
		t.Fatalf("an older build's rollback was retried: %v", ups)
	}
	if got := settingsOf(t, dir); !strings.Contains(got, `"rolledBackGen":3`) {
		t.Fatalf("settings = %s", got)
	}
}

// a restart in the middle of a switch picks it up: the device came back on a
// network that cannot reach the server, and still rolls back
func TestRestartMidSwitchRollsBack(t *testing.T) {
	f := &fakeBackend{list: []wifi.Saved{{ID: 0, SSID: "HomeNet", BSSID: "any"}, {ID: 1, SSID: "Captive", BSSID: "any"}}}
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 0})
	dir := t.TempDir()
	first := openCap(t, f, dir)
	if err := applyErr(t, first, `{"enabled":true,"ssid":"Captive"}`, map[string]string{"psk": "no-server-route"}); err != nil {
		t.Fatal(err)
	}
	first.Stop()
	kept := settingsOf(t, dir)
	if !strings.Contains(kept, `"pending":{"ssid":"Captive"`) || !strings.Contains(kept, `"previousSsid":"HomeNet"`) {
		t.Fatalf("settings = %s", kept)
	}
	if strings.Contains(kept, "no-server-route") {
		t.Fatalf("settings keep the psk: %s", kept)
	}
	// the boot lands on the target, with an address and no server
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "Captive", IP: "10.0.0.5", CurrentID: 1})
	second := openCap(t, f, dir)
	got := waitFor(t, second, "rolled_back")
	if !strings.Contains(got, `"reason":"no_server"`) {
		t.Fatalf("state = %s", got)
	}
	if _, sels := f.calls(); sels[len(sels)-1] != 0 {
		t.Fatalf("selects = %v, want the previous network last", sels)
	}
	if d := f.disabled(); len(d) != 1 || d[0] != 1 {
		t.Fatalf("disabled = %v", d)
	}
}

// and a restart whose link comes back over the target finishes the switch
func TestRestartMidSwitchFinishes(t *testing.T) {
	f := &fakeBackend{list: []wifi.Saved{{ID: 0, SSID: "HomeNet", BSSID: "any"}}}
	dir := t.TempDir()
	writeSettings(t, dir, `{"enabled":true,"ssid":"HomeNetB","pending":{"ssid":"HomeNetB","targetId":1,"previousSsid":"HomeNet","previousId":0}}`)
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNetB", IP: "192.168.1.9", CurrentID: 1})
	c := openCap(t, f, dir)
	r := watchRedials(c)
	eventually(t, "a redial at the address", func() bool { return r.n.Load() >= 1 })
	c.LinkChanged(true)
	eventually(t, "the pending switch cleared", func() bool { return !strings.Contains(settingsOf(t, dir), "pending") })
	if _, sels := f.calls(); len(sels) != 0 {
		t.Fatalf("selects = %v", sels)
	}
}

// turning the capability off leaves a switch in flight to finish or roll back
func TestDisableKeepsTheSwitchSafe(t *testing.T) {
	c, f, _ := newCap(t, nil)
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 2})
	if err := applyErr(t, c, `{"enabled":true,"ssid":"HomeNetB"}`, map[string]string{"psk": "no-server-route"}); err != nil {
		t.Fatal(err)
	}
	if err := applyErr(t, c, `{"enabled":false}`, nil); err != nil {
		t.Fatal(err)
	}
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNetB", IP: "10.0.0.5", CurrentID: 0})
	eventually(t, "the rollback", func() bool {
		_, sels := f.calls()
		return len(sels) == 2 && sels[1] == 2
	})
}

func TestDefaultPriorityWhenAbsent(t *testing.T) {
	c, f, _ := newCap(t, nil)
	setLink(f, wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 0})
	if err := applyErr(t, c, `{"enabled":true,"ssid":"HomeNetB"}`, map[string]string{"psk": "Bedroom123#"}); err != nil {
		t.Fatal(err)
	}
	ups, _ := f.calls()
	if !strings.HasSuffix(ups[0], "|3000") {
		t.Fatalf("upsert = %q, want the default priority", ups[0])
	}
}

func TestStateNeverCarriesThePsk(t *testing.T) {
	c, _, _ := newCap(t, nil)
	if err := applyErr(t, c, `{"enabled":true,"ssid":"HomeNetB"}`, map[string]string{"psk": "Bedroom123#"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range drain(t, c) {
		if strings.Contains(s, "Bedroom") {
			t.Fatalf("state leaks the psk: %s", s)
		}
	}
}

func TestScanCommand(t *testing.T) {
	c, f, _ := newCap(t, nil)
	f.scan = []wifi.AP{{SSID: "HomeNetB", BSSID: "02:00:00:00:00:a3", FreqMHz: 5180, SignalDbm: -50, Security: "[WPA2-PSK-CCMP][ESS]"}}
	out, err := c.Command(context.Background(), "scan", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Networks []map[string]any `json:"networks"`
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got.Networks) != 1 || got.Networks[0]["ssid"] != "HomeNetB" {
		t.Fatalf("scan = %s err %v", out, err)
	}
	if _, err := c.Command(context.Background(), "connect", json.RawMessage(`{}`)); !errors.Is(err, capability.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Command(context.Background(), "scan", json.RawMessage(`{"x":1}`)); !errors.Is(err, capability.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestBootResettlesTheManagedNetwork(t *testing.T) {
	f := &fakeBackend{
		status: wifi.Status{State: wifi.StateCompleted, SSID: "HomeNet", IP: "192.168.1.182", CurrentID: 0},
		list:   []wifi.Saved{{ID: 2, SSID: "HomeNetB", BSSID: "any"}},
	}
	dir := t.TempDir()
	// the last run managed HomeNetB; a boot landed on the fallback
	writeSettings(t, dir, `{"enabled":true,"ssid":"HomeNetB"}`)
	openCap(t, f, dir)
	deadline := time.After(2 * time.Second)
	for {
		if _, sels := f.calls(); len(sels) >= 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("never re-selected the managed network")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestSchemasParse(t *testing.T) {
	d := fakeDesc(t)
	for _, s := range []string{d.ConfigSchema, d.StateSchema, d.Commands["scan"]} {
		if !json.Valid([]byte(s)) {
			t.Fatalf("schema not json: %s", s)
		}
	}
	if len(d.Secrets) != 1 || d.Secrets[0].Name != "psk" {
		t.Fatalf("secrets = %v", d.Secrets)
	}
}

func fakeDesc(t *testing.T) capability.Descriptor {
	t.Helper()
	c, _, _ := newCap(t, nil)
	return c.Descriptor()
}

func writeSettings(t *testing.T, dir, body string) {
	t.Helper()
	path := wifi.SettingsPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func settingsOf(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(wifi.SettingsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
