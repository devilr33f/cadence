package policy_test

import (
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/policy"
)

// a reply heard whole, closed normally with the server's leave, opens one; each fact of the device's own refuses it
// alone
func TestFollowUpFacts(t *testing.T) {
	open := policy.FollowUpFacts{Allowed: true, Window: 5 * time.Second, Normal: true, HeardWhole: true}
	if ok, reason := policy.FollowUp(open); !ok || reason != "" {
		t.Fatalf("open facts: %v %q", ok, reason)
	}
	for _, tc := range []struct {
		reason string
		flip   func(*policy.FollowUpFacts)
	}{
		{"not_allowed", func(f *policy.FollowUpFacts) { f.Allowed = false }},
		{"not_allowed", func(f *policy.FollowUpFacts) { f.Window = 0 }},
		{"error", func(f *policy.FollowUpFacts) { f.Normal = false }},
		{"muted", func(f *policy.FollowUpFacts) { f.Muted = true }},
		{"ringing", func(f *policy.FollowUpFacts) { f.Ringing = true }},
		{"not_heard", func(f *policy.FollowUpFacts) { f.HeardWhole = false }},
	} {
		f := open
		tc.flip(&f)
		if ok, reason := policy.FollowUp(f); ok || reason != tc.reason {
			t.Errorf("%+v: %v %q, want refused %q", f, ok, reason, tc.reason)
		}
	}
}
