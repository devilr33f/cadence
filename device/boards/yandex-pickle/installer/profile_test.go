package installer_test

import (
	"slices"
	"testing"

	pickle "git.shino.boo/devilreef/cadence/device/boards/yandex-pickle"
	"git.shino.boo/devilreef/cadence/device/boards/yandex-pickle/installer"
)

// the installer puts cadence where the board looks for it
func TestProfileMatchesTheBoard(t *testing.T) {
	p, paths := installer.Profile(), pickle.New("", nil).Paths()
	if p.DataRoot != paths.DataRoot {
		t.Fatalf("data root %s, the board's %s", p.DataRoot, paths.DataRoot)
	}
	// the installer's layout: bin/ and share/ from the tarball, enroll's --ca-file
	if paths.Audiod != p.DataRoot+"/bin/cadence-audiod" || paths.CABundle != p.DataRoot+"/share/ca-certificates.crt" {
		t.Fatalf("board paths %+v", paths)
	}
	if !slices.Contains(p.Recovery, p.Anchor) || len(p.Recovery) != len(p.RecoveryDaemons) {
		t.Fatalf("recovery %q, anchor %q", p.Recovery, p.Anchor)
	}
	for _, f := range p.Facts {
		if f.Want == "" || len(f.Paths) == 0 {
			t.Fatalf("fact %+v", f)
		}
	}
	// provisioning deleted the stock, so there is none to keep or restore, and pickle never needs --ota-blocked
	if p.Stock != nil || p.RequireOTABlocked || len(p.Hardening) != 0 {
		t.Fatalf("stock %+v, ota-blocked %v, hardening %d", p.Stock, p.RequireOTABlocked, len(p.Hardening))
	}
}
