package installer_test

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	midi "git.shino.boo/devilreef/cadence/device/boards/yandex-midi"
	"git.shino.boo/devilreef/cadence/device/boards/yandex-midi/installer"
	"git.shino.boo/devilreef/cadence/device/internal/install"
)

// the installer puts cadence where the board looks for it
func TestProfileMatchesTheBoard(t *testing.T) {
	p, paths := installer.Profile(), midi.New("", nil).Paths()
	if p.DataRoot != paths.DataRoot || paths.Audiod != p.DataRoot+"/bin/cadence-audiod" ||
		paths.CABundle != p.DataRoot+"/share/ca-certificates.crt" {
		t.Fatalf("data root %s, board paths %+v", p.DataRoot, paths)
	}
	if p.Board != "yandex-midi" || p.Arch != "armhf" || p.Model != "Yandex Station Midi" || p.ModelPrefix != "" {
		t.Fatalf("board %s %s, model %q prefix %q", p.Board, p.Arch, p.Model, p.ModelPrefix)
	}
	if !slices.Contains(p.Recovery, p.Anchor) || len(p.Recovery) != len(p.RecoveryDaemons) {
		t.Fatalf("recovery %q, anchor %q", p.Recovery, p.Anchor)
	}
}

// the firmware v1 inspected (its fingerprint's names and values, never its hashes), refused unless accepted
func TestProfileFirmware(t *testing.T) {
	p := installer.Profile()
	want := map[string]string{"kernel": "4.9.68", "VERSION": "3.21", "version_full": "0.369.1.46.4793639783.20261020.295"}
	if len(p.Facts) != len(want) || p.KernelFamily != "4.9." {
		t.Fatalf("facts %+v, kernel family %q", p.Facts, p.KernelFamily)
	}
	hash := regexp.MustCompile(`[0-9a-f]{32,}`)
	for _, f := range p.Facts {
		if want[f.Name] != f.Want || len(f.Paths) == 0 || hash.MatchString(f.Want) {
			t.Errorf("fact %+v", f)
		}
	}
}

// the stock stays (M2m-R2): its directory, start script, launcher and library must be there, nothing of it may be
// started from init, and no vendor file must be absent; OTA is blocked by the router alone until the hardening steps
// are inspected (M2m-R3)
func TestProfileKeepsTheStock(t *testing.T) {
	p := installer.Profile()
	s := p.Stock
	if s == nil || s.Dir != "/system/vendor/quasar" || s.Start != s.Dir+"/run.sh" || s.Launcher != s.Dir+"/quasar_launcher2" ||
		!slices.Equal(s.Files, []string{s.Dir + "/libs/libyandex_evil.so"}) {
		t.Fatalf("stock %+v", s)
	}
	if len(p.Vendor) != 0 || len(p.VendorDaemons) != 0 || !slices.Equal(p.InittabForbidden, []string{"/system/vendor/"}) {
		t.Fatalf("vendor %q, daemons %q, inittab %q", p.Vendor, p.VendorDaemons, p.InittabForbidden)
	}
	if !p.RequireOTABlocked {
		t.Fatal("a midi installs without --ota-blocked")
	}
	// the hook's bluetooth pair, which every restore path starts again
	if len(p.PlatformDaemons) != 2 || p.PlatformDaemons[0].Exe != "/usr/bin/bluealsa-aplay" ||
		!slices.Equal(p.PlatformDaemons[0].Args, []string{"-M", "disable", "-S"}) || p.PlatformDaemons[1].Exe != "/usr/bin/just-works-bluez-agent" {
		t.Fatalf("platform daemons %+v", p.PlatformDaemons)
	}
	hook := read(t, "../files/board-hook")
	for _, d := range p.PlatformDaemons {
		if !strings.Contains(hook, d.Exe) {
			t.Errorf("the hook does not stop %s", d.Exe)
		}
	}
	for _, f := range append(slices.Clone(s.Files), s.Start, s.Launcher) {
		if !strings.HasPrefix(f, s.Dir+"/") {
			t.Errorf("%s is outside %s", f, s.Dir)
		}
	}
}

// the three hackalice steps are declared and none is enabled: each checks as not inspected and refuses to change
// anything, so the installer never applies one
func TestProfileHardeningIsNotInspected(t *testing.T) {
	ctx := context.Background()
	var names []string
	for _, s := range installer.Profile().Hardening {
		names = append(names, s.Name())
		if state, err := s.Check(ctx, nil); state != install.StepNotInspected || err != nil {
			t.Errorf("%s: %s %v", s.Name(), state, err)
		}
		if err := s.Apply(ctx, nil, "/data/cadence/backup/"+s.Name()); !errors.Is(err, install.ErrRefused) {
			t.Errorf("%s applied: %v", s.Name(), err)
		}
		if err := s.Revert(ctx, nil, "/data/cadence/backup/"+s.Name()); !errors.Is(err, install.ErrRefused) {
			t.Errorf("%s reverted: %v", s.Name(), err)
		}
	}
	if !slices.Equal(names, []string{"ota-key", "ota-script-stub", "updatesd-launch"}) {
		t.Fatalf("steps %q", names)
	}
}
