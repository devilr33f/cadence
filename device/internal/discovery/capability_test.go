package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

// fakeAdvertiser stands for Advertise: it is ready at once and runs until its ctx is done, or fails the first
// failures starts
type fakeAdvertiser struct {
	starts   atomic.Int32
	failures int32
	running  atomic.Int32
}

func (f *fakeAdvertiser) run(ctx context.Context, _ Info, _ *slog.Logger, ready func()) error {
	if f.starts.Add(1) <= f.failures {
		return errNoInterface
	}
	f.running.Add(1)
	defer f.running.Add(-1)
	ready()
	<-ctx.Done()
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func waitState(t *testing.T, c *Capability, want status) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case b := <-c.State():
			var got status
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("no state %+v", want)
		}
	}
}

func TestOnByDefaultAndAdvertising(t *testing.T) {
	f := &fakeAdvertiser{}
	c := newCapability(context.Background(), testInfo, filepath.Join(t.TempDir(), "state", "discovery"), quiet(), f.run, time.Hour)
	defer c.Stop()
	waitState(t, c, status{Enabled: true, Advertising: true})
	if d := c.Descriptor(); d.ID != "network.discovery" || d.Version != 1 {
		t.Fatalf("descriptor %+v", d)
	}
}

// the owner's off holds across a restart, before any link
func TestOffIsKeptAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "discovery")
	f := &fakeAdvertiser{}
	c := newCapability(context.Background(), testInfo, path, quiet(), f.run, time.Hour)
	waitState(t, c, status{Enabled: true, Advertising: true})
	if err := c.Apply(context.Background(), 1, json.RawMessage(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, status{Enabled: false, Advertising: false})
	if f.running.Load() != 0 {
		t.Fatal("still advertising after off")
	}
	c.Stop()

	again := newCapability(context.Background(), testInfo, path, quiet(), f.run, time.Hour)
	defer again.Stop()
	waitState(t, again, status{Enabled: false, Advertising: false})
	if f.starts.Load() != 1 {
		t.Fatalf("advertiser started %d times; the kept off was not honoured", f.starts.Load())
	}
	if err := again.Apply(context.Background(), 2, json.RawMessage(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	waitState(t, again, status{Enabled: true, Advertising: true})
}

// an omitted key keeps the current value; anything else than {enabled: bool} is refused and changes nothing
func TestConfigIsCheckedStrictly(t *testing.T) {
	f := &fakeAdvertiser{}
	c := newCapability(context.Background(), testInfo, filepath.Join(t.TempDir(), "d"), quiet(), f.run, time.Hour)
	defer c.Stop()
	waitState(t, c, status{Enabled: true, Advertising: true})
	if err := c.Apply(context.Background(), 1, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"enabled":null}`, `{"enabled":"no"}`, `{"name":"x"}`, `[]`, `{"enabled":false} {}`, ``} {
		err := c.Apply(context.Background(), 2, json.RawMessage(bad))
		var ae *capability.ApplyError
		if !errors.As(err, &ae) || ae.Code != "invalid" {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if f.running.Load() != 1 {
		t.Fatal("a refused config changed the advertiser")
	}
}

// a start that fails (no interface yet, the port taken) is tried again
func TestFailedStartIsRetried(t *testing.T) {
	f := &fakeAdvertiser{failures: 2}
	c := newCapability(context.Background(), testInfo, filepath.Join(t.TempDir(), "d"), quiet(), f.run, 10*time.Millisecond)
	defer c.Stop()
	waitState(t, c, status{Enabled: true, Advertising: true})
	if f.starts.Load() != 3 {
		t.Fatalf("starts %d", f.starts.Load())
	}
}

func TestUnreadableSettingsFallBackToOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvertiser{}
	c := newCapability(context.Background(), testInfo, path, quiet(), f.run, time.Hour)
	defer c.Stop()
	waitState(t, c, status{Enabled: true, Advertising: true})
}

func TestStopEndsTheAdvertiser(t *testing.T) {
	f := &fakeAdvertiser{}
	c := newCapability(context.Background(), testInfo, filepath.Join(t.TempDir(), "d"), quiet(), f.run, time.Hour)
	waitState(t, c, status{Enabled: true, Advertising: true})
	c.Stop()
	if f.running.Load() != 0 {
		t.Fatal("advertiser outlived Stop")
	}
}
