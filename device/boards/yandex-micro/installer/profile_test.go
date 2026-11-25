package installer

import "testing"

func TestProfileMatchesProbedLite(t *testing.T) {
	p := Profile()
	if p.Board != "yandex-micro" || p.Arch != "armhf" || p.Model != "Yandex Station Micro v1.2" ||
		p.MixerControl != "Master" || p.SpareKB != 16<<10 || !p.RequireOTABlocked || p.Stock == nil {
		t.Fatalf("profile = %+v", p)
	}
	if len(p.Recovery) != 0 || len(p.RecoveryDaemons) != 1 || p.RecoveryDaemons[0] != "adbd" {
		t.Fatalf("recovery = %q %q", p.Recovery, p.RecoveryDaemons)
	}
	if len(p.Facts) != 3 || p.Facts[2].Want != "0.369.1.48.4790768027.20261019.297" {
		t.Fatalf("facts = %+v", p.Facts)
	}
}
