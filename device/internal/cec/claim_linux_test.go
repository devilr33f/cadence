//go:build linux

package cec

import "testing"

// the pi with no hdmi sink: our claim is kept (one address asked for) but unallocated (an empty mask), and must not be
// claimed again at each reread
func TestNeedsClaimOnlyWhenNothingConfigured(t *testing.T) {
	if !needsClaim(cecLogAddrs{}) {
		t.Fatal("an unconfigured adapter is not claimed")
	}
	if needsClaim(cecLogAddrs{numLogAddrs: 1}) {
		t.Fatal("a configured claim waiting for a sink (mask 0) is claimed again")
	}
	if needsClaim(cecLogAddrs{numLogAddrs: 1, logAddrMask: 1 << 4}) {
		t.Fatal("an allocated claim is claimed again")
	}
}
