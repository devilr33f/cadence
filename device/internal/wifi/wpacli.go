package wifi

import (
	"context"
	"errors"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// errWpa is every wpa_cli refusal: its reply was FAIL, it was missing, or it
// answered nothing parseable. the caller's code says wpa, never the text.
var errWpa = errors.New("wifi: wpa_cli failed")

// WpaCLI is a Backend over wpa_supplicant's control socket: the yandex boards
// run plain wpa_supplicant with update_config=1, its conf on the data
// partition, and wpa_cli beside it (no android framework wifi: nothing else
// rewrites the conf, and save_config persists).
type WpaCLI struct {
	cli   string // wpa_cli's path, /usr/sbin/wpa_cli
	iface string
	// CtrlDir is wpa_supplicant's control directory, "" for wpa_cli's own
	// default (/var/run/wpa_supplicant).
	CtrlDir string
}

// NewWpaCLI answers a Backend over wpa_cli at cli for iface.
func NewWpaCLI(cli, iface string) *WpaCLI { return &WpaCLI{cli: cli, iface: iface} }

// run answers the reply lines of one wpa_cli command.
func (w *WpaCLI) run(ctx context.Context, args ...string) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	full := append([]string{}, args...)
	if w.iface != "" {
		full = append([]string{"-i", w.iface}, full...)
	}
	if w.CtrlDir != "" {
		full = append([]string{"-p", w.CtrlDir}, full...)
	}
	out, err := exec.CommandContext(cctx, w.cli, full...).Output() //nolint:gosec // the board's wpa_cli, argv only
	if cctx.Err() != nil || err != nil {
		return nil, errWpa
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" || strings.HasPrefix(l, "Selected interface") {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 || lines[len(lines)-1] == "FAIL" {
		return nil, errWpa
	}
	return lines, nil
}

// ok runs one command and wants OK.
func (w *WpaCLI) ok(ctx context.Context, args ...string) error {
	lines, err := w.run(ctx, args...)
	if err != nil {
		return err
	}
	if lines[len(lines)-1] != "OK" {
		return errWpa
	}
	return nil
}

func (w *WpaCLI) Status(ctx context.Context) (Status, error) {
	lines, err := w.run(ctx, "status")
	if err != nil {
		return Status{State: StateUnknown}, err
	}
	s := Status{State: StateUnknown, CurrentID: -1}
	for _, l := range lines {
		key, val, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch key {
		case "wpa_state":
			switch val {
			case "COMPLETED":
				s.State = StateCompleted
			case "ASSOCIATING":
				s.State = StateAssociating
			case "SCANNING":
				s.State = StateScanning
			case "DISCONNECTED":
				s.State = StateDisconnected
			case "INACTIVE":
				s.State = StateInactive
			}
		case "ssid":
			s.SSID = val
		case "bssid":
			s.BSSID = strings.ToLower(val)
		case "freq":
			s.FreqMHz, _ = strconv.Atoi(val)
		case "ip_address":
			s.IP = val
		case "id":
			s.CurrentID, _ = strconv.Atoi(val)
		}
	}
	if s.State != StateCompleted {
		s.SSID, s.BSSID, s.FreqMHz, s.CurrentID = "", "", 0, -1
	}
	return s, nil
}

func (w *WpaCLI) List(ctx context.Context) ([]Saved, error) {
	lines, err := w.run(ctx, "list_networks")
	if err != nil {
		return nil, err
	}
	var out []Saved
	for _, l := range lines {
		if strings.HasPrefix(l, "network id") {
			continue
		}
		f := strings.Split(l, "\t")
		if len(f) < 3 {
			continue
		}
		id, err := strconv.Atoi(f[0])
		if err != nil {
			return nil, errWpa
		}
		disabled := len(f) > 3 && strings.Contains(f[3], "[DISABLED]")
		out = append(out, Saved{ID: id, SSID: f[1], BSSID: strings.ToLower(f[2]), Disabled: disabled})
	}
	return out, nil
}

func (w *WpaCLI) Upsert(ctx context.Context, ssid, psk, bssid string, priority int) (int, error) {
	saved, err := w.List(ctx)
	if err != nil {
		return -1, err
	}
	id := -1
	for _, n := range saved {
		// by ssid alone: a changed bssid pin rewrites the entry rather than
		// adding a second for the same network
		if n.SSID == ssid {
			id = n.ID
			break
		}
	}
	if id < 0 {
		lines, err := w.run(ctx, "add_network")
		if err != nil {
			return -1, err
		}
		id, err = strconv.Atoi(lines[len(lines)-1])
		if err != nil {
			return -1, errWpa
		}
	}
	quoted := func(v string) string { return "\"" + v + "\"" }
	steps := [][]string{
		{"set_network", strconv.Itoa(id), "ssid", quoted(ssid)},
		{"set_network", strconv.Itoa(id), "psk", quoted(psk)},
		{"set_network", strconv.Itoa(id), "key_mgmt", "WPA-PSK"},
	}
	if bssid != "" {
		steps = append(steps, []string{"set_network", strconv.Itoa(id), "bssid", strings.ToLower(bssid)})
	} else {
		steps = append(steps, []string{"set_network", strconv.Itoa(id), "bssid", quoted("")})
	}
	steps = append(steps,
		[]string{"set_network", strconv.Itoa(id), "priority", strconv.Itoa(priority)},
		[]string{"enable_network", strconv.Itoa(id)},
		[]string{"save_config"},
	)
	for _, s := range steps {
		if err := w.ok(ctx, s...); err != nil {
			return id, err
		}
	}
	return id, nil
}

func (w *WpaCLI) Select(ctx context.Context, id int) error {
	// select_network disables the others in memory; enabling back the ones
	// that were enabled keeps the fallback alive before save_config persists
	// both states, while a network disabled before (one a rollback gave up
	// on) stays disabled. the target is enabled by select_network itself
	saved, err := w.List(ctx)
	if err != nil {
		return err
	}
	if err := w.ok(ctx, "select_network", strconv.Itoa(id)); err != nil {
		return err
	}
	for _, n := range saved {
		if n.Disabled || n.ID == id {
			continue
		}
		if err := w.ok(ctx, "enable_network", strconv.Itoa(n.ID)); err != nil {
			return err
		}
	}
	return w.ok(ctx, "save_config")
}

// Disable keeps the network saved but out of the supplicant's own choice.
func (w *WpaCLI) Disable(ctx context.Context, id int) error {
	if err := w.ok(ctx, "disable_network", strconv.Itoa(id)); err != nil {
		return err
	}
	return w.ok(ctx, "save_config")
}

func (w *WpaCLI) Scan(ctx context.Context) ([]AP, error) {
	if err := w.ok(ctx, "scan"); err != nil {
		return nil, err
	}
	// the scan is asynchronous: the results follow in a few seconds
	deadline := time.Now().Add(12 * time.Second)
	for {
		lines, err := w.run(ctx, "scan_results")
		if err == nil {
			return parseScan(lines), nil
		}
		if time.Now().After(deadline) {
			return nil, errWpa
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func parseScan(lines []string) []AP {
	var out []AP
	for _, l := range lines {
		if strings.HasPrefix(l, "bssid /") {
			continue
		}
		f := strings.Split(l, "\t")
		if len(f) < 5 {
			continue
		}
		freq, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		sig, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		out = append(out, AP{
			BSSID: strings.ToLower(f[0]), FreqMHz: freq, SignalDbm: sig,
			Security: f[3], SSID: strings.Trim(f[4], "\x00"),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SignalDbm > out[j].SignalDbm })
	return out
}

// WpaCLIPath is where the yandex boards keep wpa_cli.
const WpaCLIPath = "/usr/sbin/wpa_cli"

var _ Backend = (*WpaCLI)(nil)
