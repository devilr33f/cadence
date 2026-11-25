package evdev

import "testing"

// EVIOCGSW(len) for the 3 bytes SW_MAX needs, as <linux/input.h> spells it on arm and arm64
func TestEviocgswRequest(t *testing.T) {
	if got := eviocgsw(swMax/8 + 1); got != 0x8003451b {
		t.Fatalf("EVIOCGSW(3) = %#x", got)
	}
}
