package wifi_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/wifi"
)

// fake answers from a queue: each call pops one block (lines up to %%). the
// fake wpa_cli records its arguments and prints the block.
type fake struct {
	t   *testing.T
	dir string
}

func newFake(t *testing.T) *fake {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake wpa_cli is a shell script")
	}
	dir := t.TempDir()
	t.Setenv("FAKE_STATE", dir)
	return &fake{t: t, dir: dir}
}

// queue answers the next calls, each block one call's reply; "" means OK
func (f *fake) queue(replies ...string) {
	f.t.Helper()
	blocks := make([]string, 0, len(replies)*2)
	for _, r := range replies {
		if r == "" {
			r = "OK"
		}
		blocks = append(blocks, r, "%%")
	}
	if err := os.WriteFile(filepath.Join(f.dir, "replies"), []byte(strings.Join(blocks, "\n")+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// calls reads the recorded arguments, one call a line
func (f *fake) calls() []string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "calls"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		f.t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (f *fake) backend() wifi.Backend {
	return wifi.NewWpaCLI(filepath.Join("testdata", "wpa-cli"), "wlan0")
}

func TestStatusParses(t *testing.T) {
	f := newFake(t)
	f.queue(`bssid=02:00:00:00:00:a3
freq=5180
ssid=HomeNetB
id=1
mode=station
wpa_state=COMPLETED
ip_address=192.168.1.182`)
	st, err := f.backend().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := wifi.Status{State: wifi.StateCompleted, SSID: "HomeNetB", BSSID: "02:00:00:00:00:a3",
		FreqMHz: 5180, IP: "192.168.1.182", CurrentID: 1}
	if st != want {
		t.Fatalf("status = %+v, want %+v", st, want)
	}
}

func TestStatusNotCompletedHasNoAssociation(t *testing.T) {
	f := newFake(t)
	f.queue(`bssid=00:00:00:00:00:00
ssid=HomeNetB
id=1
wpa_state=SCANNING
ip_address=192.168.1.182`)
	st, err := f.backend().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// the interface keeps its address while the supplicant reassociates
	if st.State != wifi.StateScanning || st.SSID != "" || st.CurrentID != -1 || st.IP != "192.168.1.182" {
		t.Fatalf("status = %+v", st)
	}
}

func TestUpsertAddsQuoted(t *testing.T) {
	f := newFake(t)
	f.queue(
		"network id / ssid / bssid / flags\n0\tHomeNet\tany\t", // list
		"1",                        // add
		"", "", "", "", "", "", "", // set_network x6, enable, save
	)
	id, err := f.backend().Upsert(context.Background(), "HomeNetB", "Bedroom123#", "02:00:00:00:00:a3", 3333)
	if err != nil || id != 1 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	got := f.calls()
	want := []string{
		"-i wlan0 list_networks",
		"-i wlan0 add_network",
		`-i wlan0 set_network 1 ssid "HomeNetB"`,
		`-i wlan0 set_network 1 psk "Bedroom123#"`,
		"-i wlan0 set_network 1 key_mgmt WPA-PSK",
		"-i wlan0 set_network 1 bssid 02:00:00:00:00:a3",
		"-i wlan0 set_network 1 priority 3333",
		"-i wlan0 enable_network 1",
		"-i wlan0 save_config",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls =\n%q\nwant\n%q", got, want)
	}
}

func TestUpsertUnpinnedResetsBssid(t *testing.T) {
	f := newFake(t)
	f.queue(
		"network id / ssid / bssid / flags\n0\tHomeNetB\t02:00:00:00:00:a3\t", // list: same ssid, now unpinned
		"", "", "", "", "", "", "",
	)
	if _, err := f.backend().Upsert(context.Background(), "HomeNetB", "Bedroom123#", "", 3000); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.calls(), "\n")
	if !strings.Contains(got, `set_network 0 bssid ""`) {
		t.Fatalf("no bssid reset in %q", got)
	}
	if strings.Contains(got, "add_network") {
		t.Fatalf("added a second network for the same ssid: %q", got)
	}
}

func TestSelectKeepsOthersEnabled(t *testing.T) {
	f := newFake(t)
	f.queue(
		"network id / ssid / bssid / flags\n0\tHomeNet\tany\t\n1\tHomeNetB\t02:00:00:00:00:a3\t[CURRENT]\n"+
			"2\tbee76\tany\t[DISABLED]", // list
		"",     // select
		"", "", // enable 0, save
	)
	if err := f.backend().Select(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	// the disabled network (a rollback's) stays disabled
	want := "-i wlan0 list_networks\n-i wlan0 select_network 1\n-i wlan0 enable_network 0\n-i wlan0 save_config"
	if got := strings.Join(f.calls(), "\n"); got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestDisableKeepsTheNetwork(t *testing.T) {
	f := newFake(t)
	f.queue("", "")
	if err := f.backend().Disable(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	want := "-i wlan0 disable_network 2\n-i wlan0 save_config"
	if got := strings.Join(f.calls(), "\n"); got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestScanSortsBySignal(t *testing.T) {
	f := newFake(t)
	f.queue(
		"", // scan
		"bssid / frequency / signal level / flags / ssid\n02:00:00:00:00:a2\t2412\t-43\t[WPA2-PSK-CCMP][ESS]\tHomeNetB\n"+
			"02:00:00:00:00:a3\t5180\t-50\t[WPA2-PSK-CCMP][ESS]\tHomeNetB\n02:00:00:00:00:03\t2437\t-61\t[WPA2-PSK-CCMP][ESS]\t",
	)
	aps, err := f.backend().Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(aps) != 3 {
		t.Fatalf("aps = %+v", aps)
	}
	if aps[0].BSSID != "02:00:00:00:00:a2" || aps[0].SignalDbm != -43 || aps[0].SSID != "HomeNetB" {
		t.Fatalf("first = %+v", aps[0])
	}
	if aps[2].SSID != "" || aps[2].FreqMHz != 2437 {
		t.Fatalf("hidden = %+v", aps[2])
	}
}

func TestFailIsOpaque(t *testing.T) {
	f := newFake(t)
	f.queue("FAIL")
	if _, err := f.backend().Status(context.Background()); err == nil || err.Error() != "wifi: wpa_cli failed" {
		t.Fatalf("err = %v", err)
	}
}
