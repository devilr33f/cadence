package discovery_test

import (
	"net/netip"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/discovery"
)

// the host's own addresses: at most 4, each an IPv4 one that is neither loopback nor link-local
func TestLANAddressesAreLANIPv4(t *testing.T) {
	got := discovery.LANAddresses()
	if len(got) > 4 {
		t.Fatalf("addresses = %v", got)
	}
	for _, s := range got {
		a, err := netip.ParseAddr(s)
		if err != nil || !a.Is4() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
			t.Fatalf("address %q of %v", s, got)
		}
	}
}
