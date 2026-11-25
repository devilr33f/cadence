// Package audio is the seam between the session controller and a board's
// microphone and speaker.
package audio

import (
	"context"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// Trigger asks the session controller for a session, or, as its Mode says, to
// end one. Fixture names the canned utterance a sim board's microphone replays,
// and Test says test audio speaks in the session (the sim's say file, or a
// follow-up of a session it spoke in): a command word heard while it does never
// acts, and no ring lets it in. Real boards set neither.
type Trigger struct {
	Kind    cadencev2.Trigger
	Fixture string
	Test    bool
	Mode    Mode
	// what heard the wake word, for a wake
	Wake *cadencev2.Wake
	// a custom wake word's (wake.custom) instant in unix ms on the device clock: the capture starts with the
	// microphone audiod kept from then on, so a command said right after the word is heard. 0 for none
	PrerollFromMs int64
	// the ring a wake stopped before its session began
	AfterRing *cadencev2.AfterRing
	// when the trigger happened (a wake: when audiod heard it); zero is when the session begins. the turn's span starts
	// there
	At time.Time
	// the session a CommandEnd ends; for an activation, the id the server gave the session it asked for (MicOpen),
	// else the controller makes one
	Session string
}

// Mode says what a trigger does about the session that runs.
type Mode uint8

const (
	// Supersede ends the session that runs, if any, and begins another.
	Supersede Mode = iota
	// Toggle cancels the session that runs instead of beginning one: the action button.
	Toggle
	// IfIdle is ignored while a session runs, where speech over the reply is barge-in: a wake.
	IfIdle
	// Mute begins nothing: it cancels the session that runs and cuts a reply that still plays (the mute switch).
	Mute
	// Ring begins nothing: it cancels the session that runs and cuts a reply that still plays and the announcement (an
	// alarm or a timer begins to ring).
	Ring
	// CommandEnd begins nothing: it cancels the session Session names, if it still runs, with its reply. A command word
	// the speaker acted on reached that session's microphone too, where it would barge in and be answered.
	CommandEnd
)

// Mic yields opus packets 20 ms apart and closes the channel at the end.
type Mic interface {
	Open(ctx context.Context, t Trigger) (<-chan []byte, error)
}

type StreamWriter interface {
	Write(seq uint32, opus []byte) error
	End() error
	// Abort cuts the stream at once and never waits, also after End while the
	// reply still plays; it is idempotent.
	Abort()
}

type Speaker interface {
	Begin(s *cadencev2.StreamStart) (StreamWriter, error)
}

type IO struct {
	Triggers <-chan Trigger
	Mic      Mic
	Speaker  Speaker
}

// MicFailureReporter is optionally implemented by a Mic: after the packets
// channel it opened closes, MicFailure tells a failed capture (the device died
// or never produced audio) from a normal stop.
type MicFailureReporter interface {
	MicFailure(packets <-chan []byte) bool
}

// Ducker is optionally implemented by a Speaker that plays music: Duck(true)
// lowers it while a session is open, Duck(false) lets it come back.
type Ducker interface {
	Duck(active bool)
}

// WakeEarconer is optionally implemented by a Speaker that has the wake earcon: WakeEarcon plays it once, at a
// session the owner opened (a wake word or a press), so they hear that the speaker listens.
type WakeEarconer interface {
	WakeEarcon()
}

// ErrorSounder is optionally implemented by a Speaker that tells the owner a request failed: ErrorSound plays the
// spoken prompt id ("offline", "failed", "not_heard") in lang once, or the error earcon for "" or a prompt the device
// lacks. It never touches the level.
type ErrorSounder interface {
	ErrorSound(id, lang string)
}
