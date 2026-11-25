package input

import (
	"regexp"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// v1: the wake engine is not trusted for its first 3 s after the capture (re)opens
const reopenGuard = 3 * time.Second

// a wake older than this is from another clock: its turn starts when cadenced heard it
const maxWakeAge = 10 * time.Second

// a spotter that failed leaves the vqe serving button sessions, and nothing wakes
const spotterFailed = "spotter_failed"

var tokenChars = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// InputReady tells a new audiod where the switch stands, before any capture can start there; nothing wakes until its
// VendorStatus says the engines run. It runs under the supervisor's lock and never calls it.
func (in *Input) InputReady(send func(*audiodv1.Envelope) error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.send, in.wake = send, false
	in.sendMute()
	if in.listener != nil {
		in.listener.ListenReady(send)
	}
	in.publish()
}

// InputGone follows each InputReady once audiod's connection ended.
func (in *Input) InputGone() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.send, in.wake = nil, false
	if in.listener != nil {
		in.listener.ListenGone()
	}
	in.publish()
}

// InputEnvelope takes each WakeEvent, CommandWord, VendorStatus and MicPcm; it never blocks.
func (in *Input) InputEnvelope(env *audiodv1.Envelope) {
	switch b := env.GetBody().(type) {
	case *audiodv1.Envelope_MicPcm:
		in.mu.Lock()
		l := in.listener
		in.mu.Unlock()
		if l != nil {
			l.MicPCM(b.MicPcm)
		}
	case *audiodv1.Envelope_WakeEvent:
		in.wakeEvent(b.WakeEvent)
	case *audiodv1.Envelope_CommandWord:
		in.commandWord(b.CommandWord)
	case *audiodv1.Envelope_VendorStatus:
		in.vendorStatus(b.VendorStatus)
	}
}

// wakeEvent begins a session unless muted, without a wake engine, or within 3 s of the capture opening; a session
// that runs ignores it (the controller's part)
func (in *Input) wakeEvent(w *audiodv1.WakeEvent) {
	if in.counts != nil {
		in.counts.WakeHit()
	}
	now := in.now()
	in.mu.Lock()
	defer in.mu.Unlock()
	reason := ""
	switch {
	case in.muted:
		reason = "muted"
	case !in.wake:
		reason = "unavailable"
	case now.Sub(in.opened) < reopenGuard:
		reason = "reopen"
	}
	if reason != "" {
		in.log.Info("wake_ignored", "reason", reason)
		return
	}
	wake := &cadencev2.Wake{Engine: token(w.GetEngine()), Score: w.GetScore()}
	in.emit(audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle, Wake: wake, At: wakeAt(w, now)})
}

// the wake's own instant, nanoseconds when audiod sends them; one that strays past maxWakeAge from now (another
// clock, or none) is now
func wakeAt(w *audiodv1.WakeEvent, now time.Time) time.Time {
	at := time.UnixMilli(w.GetAtMs())
	if ns := w.GetAtUnixNs(); ns > 0 {
		at = time.Unix(0, ns)
	}
	if w.GetAtMs() <= 0 && w.GetAtUnixNs() <= 0 || at.After(now) || now.Sub(at) > maxWakeAge {
		return now
	}
	return at
}

// commandWord hands the word to the policy, outside mu, and counts what it did, by id: a phrase never crosses here
func (in *Input) commandWord(cw *audiodv1.CommandWord) {
	in.mu.Lock()
	p, muted := in.commands, in.muted
	in.mu.Unlock()
	action, reason := "", "unavailable"
	if p != nil {
		action, reason = p.Command(cw.GetId(), muted)
	}
	if in.counts != nil {
		in.counts.CommandWord(action != "")
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if action != "" {
		in.acted++
		in.log.Info("command", "id", cw.GetId(), "action", action, "count", in.acted)
		return
	}
	in.ignored++
	in.log.Info("command_ignored", "id", cw.GetId(), "reason", reason, "count", in.ignored)
}

// vendorStatus says whether the engines hear the wake word: with them off, or a spotter that failed, the button alone
// begins sessions
func (in *Input) vendorStatus(vs *audiodv1.VendorStatus) {
	wake := vs.GetEngines() && vs.GetCode() != spotterFailed
	now := in.now()
	in.mu.Lock()
	defer in.mu.Unlock()
	in.wake = wake
	if vs.GetEngines() {
		// the engines just opened the capture
		in.opened = now
	}
	in.publish()
	in.log.Info("wake_status", "available", wake, "code", token(vs.GetCode()))
}

// token passes audiod's token on, and nothing else it might send
func token(s string) string {
	if s == "" || tokenChars.MatchString(s) {
		return s
	}
	return "unknown"
}
