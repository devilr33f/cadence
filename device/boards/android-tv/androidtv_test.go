package androidtv_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	androidtv "git.shino.boo/devilreef/cadence/device/boards/android-tv"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/discovery"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

const stick = "Xiaomi/mitv_mdz_24/mdz-24-aa:11/RTT0.211222.001/12345:user/release-keys"

func TestFingerprintIsTheApps(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want error
	}{
		{nil, board.ErrFingerprint},
		{map[string]string{"CADENCE_ANDROID_FINGERPRINT": stick}, board.ErrFingerprint},
		{map[string]string{"CADENCE_ANDROID_TV": "1", "CADENCE_ANDROID_FINGERPRINT": "nonsense"}, board.ErrFingerprint},
		{map[string]string{"CADENCE_ANDROID_TV": "1", "CADENCE_ANDROID_FINGERPRINT": stick}, nil},
	} {
		if err := androidtv.New(env(c.env), t.TempDir(), nil).Fingerprint(context.Background()); !errors.Is(err, c.want) {
			t.Fatalf("%v: err = %v", c.env, err)
		}
	}
}

// no kernel log an app may read
func TestTelemetryProfile(t *testing.T) {
	dir := t.TempDir()
	p := androidtv.New(env(nil), dir, nil).Telemetry()
	if p.Kmsg || p.TempSensor != "" || len(p.Mounts) != 1 || p.Mounts[0] != dir {
		t.Fatalf("profile = %+v", p)
	}
}

// audiod runs in the app: the board dials it (the supervisor's external mode) and has a speaker
func TestAudiodIsTheApps(t *testing.T) {
	b := androidtv.New(env(nil), t.TempDir(), nil)
	if _, ok := b.AudioIO(); !ok {
		t.Fatal("no audio io: the app's audiod is not dialled")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

// the app's NsdManager registers what cadenced would advertise, from the file it hands over
func TestAdvertiseHandsOffToTheApp(t *testing.T) {
	dir := t.TempDir()
	b := androidtv.New(env(nil), dir, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- b.Advertise(ctx, discovery.Info{DeviceID: "dev-1", Board: androidtv.ID, Version: "v1"},
			slog.New(slog.NewTextHandler(io.Discard, nil)), func() { close(ready) })
	}()
	<-ready
	raw, err := os.ReadFile(androidtv.HandoffPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var h discovery.Handoff
	if err := json.Unmarshal(raw, &h); err != nil || h.TXT["id"] != "dev-1" || h.TXT["board"] != androidtv.ID {
		t.Fatalf("handoff %s: %v", raw, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// media.soloist is there only on a kernel that can run spotify's arm64 build: the app names the machine, and a
// 32-bit kernel (or none named) leaves the capability away entirely
func TestSoloistNeedsAnArm64Kernel(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{"CADENCE_ANDROID_KERNEL_MACHINE": "aarch64"}, true},
		{map[string]string{"CADENCE_ANDROID_KERNEL_MACHINE": "armv7l"}, false},
	} {
		b := androidtv.New(env(c.env), t.TempDir(), nil)
		var found bool
		for _, p := range b.Capabilities() {
			if p.Descriptor().ID == "media.soloist" {
				found = true
			}
		}
		if found != c.want {
			t.Fatalf("%v: media.soloist = %v", c.env, found)
		}
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// with the capability there, a config without a key leaves soloist key_missing, as on the speakers
func TestSoloistCapability(t *testing.T) {
	b := androidtv.New(env(map[string]string{"CADENCE_ANDROID_KERNEL_MACHINE": "aarch64"}), t.TempDir(), nil)
	defer func() { _ = b.Close() }()
	var soloistCap capability.Provider
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID == "media.soloist" {
			soloistCap = p
		}
	}
	if soloistCap == nil {
		t.Fatal("no media.soloist")
	}
	if err := soloistCap.Apply(context.Background(), 1, json.RawMessage(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case st := <-soloistCap.State():
		if !strings.Contains(string(st), `"state":"key_missing"`) {
			t.Fatalf("state %s", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no state")
	}
}
