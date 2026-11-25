//go:build unix

package discovery

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"
)

// a real advertiser on a test port is found by a real browse over the host's multicast interfaces, and stops
// answering once stopped
func TestAdvertiseIsFoundByBrowse(t *testing.T) {
	if len(multicastIfaces()) == 0 {
		t.Skip("no multicast interface")
	}
	const port = 53530
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- advertise(ctx, testInfo, log, func() { close(ready) }, port, timings{announceGap: 10 * time.Millisecond, recheck: time.Hour})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Skipf("cannot advertise here: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("advertiser not ready")
	}
	dst := netip.AddrPortFrom(group, port)
	got, err := browse(context.Background(), 1500*time.Millisecond, dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		cancel()
		t.Logf("advertiser ended: %v", <-done)
		t.Skip("multicast does not loop back on this host")
	}
	if len(got) != 1 || got[0].DeviceID != testInfo.DeviceID || got[0].Board != "yandex-pickle" || len(got[0].Addrs) == 0 {
		t.Fatalf("browse %+v", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if again, _ := browse(context.Background(), 500*time.Millisecond, dst); len(again) != 0 {
		t.Fatalf("still answering after stop: %+v", again)
	}
}
