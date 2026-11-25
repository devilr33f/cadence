package deviceops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/electricbubble/gadb"
)

// adbTransport drives a device through an adb server (the pod's sidecar) with gadb, a pure-Go
// client of the adb server protocol: connect over tcp, shell, sync push. Our code never runs an
// adb binary; the sidecar owns the keys (ADB_VENDOR_KEYS) the devices already trust.
type adbTransport struct {
	server string // host:port of the adb server
}

// longShell is gadb's read deadline for one wrapped command: install.sh's apt step can take a
// while on a first provision.
const longShell = 10 * time.Minute

// adbMu guards gadb's package-level read deadline across parallel runs.
var adbMu sync.Mutex

func (t *adbTransport) device(_ context.Context, acc access) (gadb.Client, gadb.Device, error) {
	if t.server == "" {
		return gadb.Client{}, gadb.Device{}, errors.New("deviceops: no adb server")
	}
	host, portStr, err := net.SplitHostPort(t.server)
	if err != nil {
		return gadb.Client{}, gadb.Device{}, fmt.Errorf("deviceops: bad adb server %q", t.server)
	}
	port, _ := strconv.Atoi(portStr)
	client, err := gadb.NewClientWith(host, port)
	if err != nil {
		return gadb.Client{}, gadb.Device{}, fmt.Errorf("deviceops: adb server: %w", err)
	}
	ip, dport, err := net.SplitHostPort(acc.addr)
	if err != nil {
		return gadb.Client{}, gadb.Device{}, fmt.Errorf("%w: %q", ErrBadAccess, acc.addr)
	}
	pn, _ := strconv.Atoi(dport)
	if err := client.Connect(ip, pn); err != nil && !strings.Contains(err.Error(), "already connected") {
		return gadb.Client{}, gadb.Device{}, fmt.Errorf("deviceops: adb connect %s: %w", acc.addr, err)
	}
	devices, err := client.DeviceList()
	if err != nil {
		return gadb.Client{}, gadb.Device{}, fmt.Errorf("deviceops: adb devices: %w", err)
	}
	for _, d := range devices {
		if d.Serial() == acc.addr {
			return client, d, nil
		}
	}
	return gadb.Client{}, gadb.Device{}, fmt.Errorf("deviceops: adb does not see %s", acc.addr)
}

func (t *adbTransport) probe(ctx context.Context, acc access) error {
	_, out, err := t.exec(ctx, acc, "echo cadence-probe-ok", 15*time.Second)
	if err != nil {
		return err
	}
	if !strings.Contains(out, "cadence-probe-ok") {
		return fmt.Errorf("deviceops: adb probe silent")
	}
	return nil
}

func (t *adbTransport) exec(ctx context.Context, acc access, cmd string, wait time.Duration) (int, string, error) {
	_, dev, err := t.device(ctx, acc)
	if err != nil {
		return 0, "", err
	}
	out := withDeadline(wait, func() (string, error) { return dev.RunShellCommand(wrapScript(cmd)) })
	if out.err != nil {
		return 0, "", fmt.Errorf("deviceops: adb shell: %w", out.err)
	}
	code, body, ok := unwrapScript(out.val)
	if !ok {
		return 0, body, errNoMarker
	}
	return code, body, nil
}

// withDeadline sets gadb's package-level read timeout under adbMu (parallel runs share it).
func withDeadline(wait time.Duration, fn func() (string, error)) struct {
	val string
	err error
} {
	if wait <= 0 {
		wait = longShell
	}
	adbMu.Lock()
	old := gadb.DefaultAdbReadTimeout
	gadb.DefaultAdbReadTimeout = wait
	adbMu.Unlock()
	val, err := fn()
	adbMu.Lock()
	gadb.DefaultAdbReadTimeout = old
	adbMu.Unlock()
	return struct {
		val string
		err error
	}{val, err}
}

func (t *adbTransport) writeFile(ctx context.Context, acc access, path string, mode uint32, data []byte) error {
	_, dev, err := t.device(ctx, acc)
	if err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	if badPath(path) {
		return fmt.Errorf("deviceops: refusing a path the sync layer cannot name: %q", path)
	}
	out := withDeadline(longShell, func() (string, error) {
		return "", dev.Push(bytes.NewReader(data), path, time.Now(), os.FileMode(mode))
	})
	return out.err
}

func (t *adbTransport) readHash(ctx context.Context, acc access, path string) (string, error) {
	if badPath(path) {
		return "", fmt.Errorf("deviceops: refusing a path the shell cannot name: %q", path)
	}
	_, out, err := t.exec(ctx, acc, "sha256sum '"+path+"'", 2*time.Minute)
	if err != nil {
		return "", err
	}
	f := strings.Fields(out)
	if len(f) == 0 || len(f[0]) != 64 {
		return "", fmt.Errorf("deviceops: no hash for %s", path)
	}
	return f[0], nil
}

func badPath(p string) bool { return strings.ContainsAny(p, `'"`+"`"+` ;&$()*?<>|\`) }
