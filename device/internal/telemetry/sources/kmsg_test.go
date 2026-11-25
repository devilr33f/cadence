package sources_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/sources"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/sources/kmsgtest"
)

func TestKmsgParse(t *testing.T) {
	for _, c := range []struct {
		name, rec string
		sev       logs.Severity
		body      string
	}{
		{"emergency", "0,1,10,-;panic", logs.Error, "panic"},
		{"error", "3,2,10,-;usb 1-1: device descriptor read error", logs.Error, "usb 1-1: device descriptor read error"},
		{"warning", "4,3,10,-;cpufreq: limited", logs.Warn, "cpufreq: limited"},
		{"notice", "5,4,10,-;audit: ok", logs.Info, "audit: ok"},
		{"info", "6,5,10,-;wlan0: up", logs.Info, "wlan0: up"},
		{"debug", "7,6,10,-;regulator: probe", logs.Debug, "regulator: probe"},
		// a facility above the level: daemon (3) at info, user (1) at warning
		{"facility", "30,7,10,-;dhcp: lease", logs.Info, "dhcp: lease"},
		{"user facility", "12,8,10,-;app: odd", logs.Warn, "app: odd"},
		{"more prefix fields", "6,9,10,c,caller=T1;split", logs.Info, "split"},
		{"continuation lines", "6,10,5140900,-;NET: Registered protocol family 10\n SUBSYSTEM=net\n DEVICE=+net:lo",
			logs.Info, "NET: Registered protocol family 10 SUBSYSTEM=net DEVICE=+net:lo"},
		{"a semicolon in the message", "6,11,10,-;a;b", logs.Info, "a;b"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := sources.ParseKmsg([]byte(c.rec + "\n"))
			if !ok || got.Source != "kmsg" || got.Severity != c.sev || got.Body != c.body || got.Truncated {
				t.Fatalf("parsed %+v, %v", got, ok)
			}
		})
	}
	for _, bad := range []string{"no prefix", "x,1,10,-;bad priority", "6;no sequence", ""} {
		if got, ok := sources.ParseKmsg([]byte(bad)); ok {
			t.Fatalf("%q parsed as %+v", bad, got)
		}
	}
}

// a record longer than a line is cut; the kernel never writes one, a fake could
func TestKmsgLongRecordCut(t *testing.T) {
	got, ok := sources.ParseKmsg([]byte("6,1,10,-;" + strings.Repeat("a", logs.MaxLine+10)))
	if !ok || len(got.Body) != logs.MaxLine || !got.Truncated {
		t.Fatalf("parsed %d bytes, truncated %v, ok %v", len(got.Body), got.Truncated, ok)
	}
}

type kmsgRun struct {
	file *kmsgtest.File
	rec  *recorder
	stop func() error
}

func startKmsg(t *testing.T, file *kmsgtest.File) *kmsgRun {
	t.Helper()
	rec := &recorder{}
	k := sources.NewKmsg(func() (sources.KmsgFile, error) { return file, nil }, rec)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var err error
	wg.Go(func() { err = k.Run(ctx) })
	stop := func() error {
		cancel()
		wg.Wait()
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return &kmsgRun{file: file, rec: rec, stop: stop}
}

// the boot buffer is never read again at a restart: the reader starts at the end
func TestKmsgStartsAtEnd(t *testing.T) {
	file := kmsgtest.New()
	file.Add("6,1,10,-;booted")
	file.Add("6,2,20,-;also at boot")
	r := startKmsg(t, file)
	waitFor(t, "the seek to the end", func() bool { return file.Seeks() == 1 })
	file.Add("6,3,30,-;after the start")
	waitBodies(t, r.rec, "after the start")
}

// an overrun loses records: the read goes on, and the gap is reported as one warning
func TestKmsgOverrunResumes(t *testing.T) {
	file := kmsgtest.New()
	r := startKmsg(t, file)
	waitFor(t, "the seek to the end", func() bool { return file.Seeks() == 1 })
	file.Add("6,10,10,-;before")
	file.Fail(syscall.EPIPE)
	file.Add("6,15,20,-;after")
	waitBodies(t, r.rec, "before", "dropped 4 lines", "after")
	if got := r.rec.all()[1]; got.Source != "kmsg" || got.Severity != logs.Warn {
		t.Fatalf("drop report %+v", got)
	}
}

func TestKmsgStops(t *testing.T) {
	file := kmsgtest.New()
	r := startKmsg(t, file)
	waitFor(t, "the seek to the end", func() bool { return file.Seeks() == 1 })
	done := make(chan error, 1)
	go func() { done <- r.stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stopped with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reader did not stop")
	}
	if !file.Closed() {
		t.Fatal("the file was left open")
	}
}

func TestKmsgOpenFails(t *testing.T) {
	k := sources.NewKmsg(func() (sources.KmsgFile, error) { return nil, os.ErrPermission }, &recorder{})
	if err := k.Run(context.Background()); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
}

// any other read error ends the reader, which says so
func TestKmsgReadFails(t *testing.T) {
	file := kmsgtest.New()
	r := startKmsg(t, file)
	waitFor(t, "the seek to the end", func() bool { return file.Seeks() == 1 })
	file.Fail(syscall.EIO)
	waitFor(t, "the reader to end", file.Closed)
	if err := r.stop(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("err = %v", err)
	}
}

// the real device, where this host lets the test read it: seeking to its end
// and a stop that ends a blocked read
func TestKmsgDevice(t *testing.T) {
	const dev = "/dev/kmsg"
	f, err := sources.OpenKmsg(dev)
	if err != nil {
		t.Skip("no readable kernel log here")
	}
	_ = f.Close()
	rec := &recorder{}
	k := sources.NewKmsg(func() (sources.KmsgFile, error) { return sources.OpenKmsg(dev) }, rec)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stopped with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a blocked read kept the reader")
	}
}
