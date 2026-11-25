package policy_test

import (
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/policy"
)

// the level in force goes back on the hardware only when it is the device's own and no ring ramps it
func TestVolumeReassertFacts(t *testing.T) {
	for _, tc := range []struct {
		facts policy.VolumeFacts
		want  bool
	}{
		{policy.VolumeFacts{Known: true}, true},
		{policy.VolumeFacts{Known: false}, false},
		{policy.VolumeFacts{Known: true, Ringing: true}, false},
	} {
		if got := policy.VolumeReassert(tc.facts); got != tc.want {
			t.Errorf("%+v: %v, want %v", tc.facts, got, tc.want)
		}
	}
}
