// Package policy holds the device's pure decisions about rule 7 and the voice loop: whether a reply opens a follow-up,
// what a command word does, and when the level in force goes back on the hardware. Each takes the facts its caller
// gathered and returns what to do; none touches the device.
package policy

import "time"

// NotAllowed is FollowUp's reason when the server allowed no follow-up: no refusal of the device's own.
const NotAllowed = "not_allowed"

// FollowUpFacts are what the device knows when the server closes a session (M6a-D5).
type FollowUpFacts struct {
	// the server allows a follow-up (SessionClose.follow_up_allowed) and keeps it open this long
	Allowed bool
	Window  time.Duration
	// the session closed with no error code
	Normal bool
	// the microphone switch is on
	Muted bool
	// an alarm or a timer rings
	Ringing bool
	// the player took the whole reply: every reply stream ended, each frame and end written
	HeardWhole bool
}

// FollowUp says whether the device opens a follow-up session, and why not: not_allowed (the server said none, or no
// window), error, muted, ringing or not_heard. A follow-up hears whoever talks after a reply, so the device opens one
// only after a reply the owner heard whole. Test audio refuses none (M6a-R4): no board injects it past its microphone,
// and the sim's say file, its only test audio, is its microphone and feeds its follow-ups too.
func FollowUp(f FollowUpFacts) (open bool, reason string) {
	switch {
	case !f.Allowed || f.Window <= 0:
		return false, NotAllowed
	case !f.Normal:
		return false, "error"
	case f.Muted:
		return false, "muted"
	case f.Ringing:
		return false, "ringing"
	case !f.HeardWhole:
		return false, "not_heard"
	}
	return true, ""
}
