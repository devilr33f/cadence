package session

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func (r *rig) quiet(t *testing.T, what string) {
	t.Helper()
	select {
	case env := <-r.sent:
		t.Fatalf("%s: unexpected frame %v", what, env)
	case <-time.After(50 * time.Millisecond):
	}
}

// the action button begins a session, and a press while it runs cancels it instead
func TestToggleBeginsOrCancels(t *testing.T) {
	r := newRig(t, 0)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	if s := r.next(t).GetSessionStart(); s.GetSessionId() != "s1" || s.GetTrigger() != cadencev2.Trigger_TRIGGER_BUTTON || s.GetWake() != nil {
		t.Fatalf("start = %v", s)
	}
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	if c := r.next(t).GetSessionCancel(); c.GetSessionId() != "s1" || c.GetReason() != "local_cancel" {
		t.Fatalf("cancel = %v", c)
	}
	r.quiet(t, "after the cancel")
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	if s := r.next(t).GetSessionStart(); s.GetSessionId() != "s2" {
		t.Fatalf("third press: %v", s)
	}
}

// a wake begins a session with what heard it, and is ignored while one runs: speech over a reply is barge-in
func TestWakeOnlyWhileIdle(t *testing.T) {
	r := newRig(t, 0)
	wake := &cadencev2.Wake{Engine: "stock", Score: 0.9}
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle, Wake: wake}
	s := r.next(t).GetSessionStart()
	if s.GetSessionId() != "s1" || s.GetTrigger() != cadencev2.Trigger_TRIGGER_WAKE || s.GetWake().GetEngine() != "stock" || s.GetWake().GetScore() != 0.9 {
		t.Fatalf("start = %v", s)
	}
	r.handle(t, &cadencev2.StreamStart{StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	w := <-r.speaker.writers
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle, Wake: wake}
	r.quiet(t, "wake during a session")
	if aborted(w, 10*time.Millisecond) {
		t.Fatal("an ignored wake cut the reply")
	}
	if !strings.Contains(r.logs.String(), "msg=wake_ignored session_id=s1 reason=active") {
		t.Fatalf("log: %s", r.logs)
	}

	// after the close the reply's tail plays out, and a wake cuts it with a new session
	r.handle(t, &cadencev2.StreamEnd{StreamId: "st1"})
	<-w.ended
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1"})
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle, Wake: wake}
	if s := r.next(t).GetSessionStart(); s.GetSessionId() != "s2" {
		t.Fatalf("wake after the close: %v", s)
	}
	if !aborted(w, 2*time.Second) {
		t.Fatal("the new session left the old tail playing")
	}
}

// the mute switch cancels the session that runs with its reply, and a reply tail that still plays; it begins none
func TestMuteCancelsAndBeginsNothing(t *testing.T) {
	r := newRig(t, 0)
	r.triggers <- audio.Trigger{Mode: audio.Mute}
	r.quiet(t, "mute with no session")

	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	r.next(t)
	r.handle(t, &cadencev2.StreamStart{StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	w := <-r.speaker.writers
	r.triggers <- audio.Trigger{Mode: audio.Mute}
	if c := r.next(t).GetSessionCancel(); c.GetSessionId() != "s1" || c.GetReason() != "muted" {
		t.Fatalf("cancel = %v", c)
	}
	if !aborted(w, 2*time.Second) {
		t.Fatal("mute left the reply playing")
	}
	r.quiet(t, "after the mute")

	tail := endedReplyIn(t, r, "s2")
	r.handle(t, &cadencev2.SessionClose{SessionId: "s2"})
	r.triggers <- audio.Trigger{Mode: audio.Mute}
	if !aborted(tail, 2*time.Second) {
		t.Fatal("mute left the tail playing")
	}
	r.quiet(t, "mute over a tail")
}

func endedReplyIn(t *testing.T, r *rig, id string) *fakeWriter {
	t.Helper()
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	if s := r.next(t).GetSessionStart(); s.GetSessionId() != id {
		t.Fatalf("start = %v", s)
	}
	r.handle(t, &cadencev2.StreamStart{StreamId: "t-" + id, SessionId: id, Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	w := <-r.speaker.writers
	r.handle(t, &cadencev2.StreamEnd{StreamId: "t-" + id})
	<-w.ended
	return w
}

type phases struct {
	mu  sync.Mutex
	got []Phase
}

func (p *phases) add(ph Phase) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, ph)
}

func (p *phases) wait(t *testing.T, want ...Phase) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		p.mu.Lock()
		got := slices.Clone(p.got)
		p.mu.Unlock()
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("phases = %v, want %v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// the lights follow the session: listening from its start, then what the server says, speaking with the reply, idle
// once it ends
func TestPhasesFollowTheSession(t *testing.T) {
	p := &phases{}
	r := newRig(t, 0, WithPhases(p.add))
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	r.next(t)
	p.wait(t, PhaseListening)
	r.handle(t, &cadencev2.SessionEvent{SessionId: "s1", Kind: cadencev2.SessionEventKind_SESSION_EVENT_KIND_SPEECH_STARTED})
	r.handle(t, &cadencev2.SessionEvent{SessionId: "s1", Kind: cadencev2.SessionEventKind_SESSION_EVENT_KIND_THINKING})
	// another session's event changes nothing
	r.handle(t, &cadencev2.SessionEvent{SessionId: "old", Kind: cadencev2.SessionEventKind_SESSION_EVENT_KIND_LISTENING})
	p.wait(t, PhaseListening, PhaseThinking)
	r.handle(t, &cadencev2.StreamStart{StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	<-r.speaker.writers
	p.wait(t, PhaseListening, PhaseThinking, PhaseSpeaking)
	r.handle(t, &cadencev2.SessionEvent{SessionId: "s1", Kind: cadencev2.SessionEventKind_SESSION_EVENT_KIND_LISTENING})
	p.wait(t, PhaseListening, PhaseThinking, PhaseSpeaking, PhaseListening)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1"})
	p.wait(t, PhaseListening, PhaseThinking, PhaseSpeaking, PhaseListening, PhaseIdle)

	// a press that cancels goes back to idle too
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	r.next(t)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	r.next(t)
	p.wait(t, PhaseListening, PhaseThinking, PhaseSpeaking, PhaseListening, PhaseIdle, PhaseListening, PhaseIdle)
}

// armed reports each no-speech deadline the controller arms
func (r *rig) armed() <-chan time.Duration {
	armed := make(chan time.Duration, 4)
	r.c.after = func(d time.Duration) <-chan time.Time { armed <- d; return r.clock }
	return armed
}

func noArm(t *testing.T, armed <-chan time.Duration, what string) {
	t.Helper()
	select {
	case d := <-armed:
		t.Fatalf("%s: a %v deadline armed", what, d)
	case <-time.After(50 * time.Millisecond):
	}
}

// the no-speech deadline counts from the microphone's first packet: audiod may hold a session's capture while it
// probes and starts the vendor engines, past the 8 s
func TestNoSpeechCountsFromTheFirstPacket(t *testing.T) {
	r := newRig(t, 1)
	armed := r.armed()
	hold := make(chan struct{})
	r.mic.mu.Lock()
	r.mic.hold = hold
	r.mic.mu.Unlock()
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	r.next(t)
	noArm(t, armed, "before the first packet")
	close(hold)
	if m := r.next(t).GetMicAudio(); m.GetSeq() != 0 {
		t.Fatalf("mic = %v", m)
	}
	select {
	case d := <-armed:
		if d != noSpeechWait {
			t.Fatalf("armed %v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no deadline after the first packet")
	}
	r.clock <- time.Now()
	if c := r.next(t).GetSessionCancel(); c.GetReason() != "no_speech" {
		t.Fatalf("cancel = %v", c)
	}

	// speech the server heard before the first packet leaves nothing to arm
	hold2 := make(chan struct{})
	r.mic.mu.Lock()
	r.mic.hold = hold2
	r.mic.mu.Unlock()
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	r.next(t)
	r.handle(t, &cadencev2.SessionEvent{SessionId: "s2", Kind: cadencev2.SessionEventKind_SESSION_EVENT_KIND_SPEECH_STARTED})
	close(hold2)
	r.next(t)
	noArm(t, armed, "after speech")
}

// a microphone that stops without a packet or a failure still lets the deadline end a silent session
func TestNoSpeechArmsWhenTheMicEndsSilent(t *testing.T) {
	r := newRig(t, 0)
	r.mic.ends = true
	armed := r.armed()
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	r.next(t)
	if d := <-armed; d != noSpeechWait {
		t.Fatalf("armed %v", d)
	}
	r.clock <- time.Now()
	if c := r.next(t).GetSessionCancel(); c.GetReason() != "no_speech" {
		t.Fatalf("cancel = %v", c)
	}
}

// an alarm or a timer that begins to ring ends the session that runs with a cancel the server sees, cutting its reply,
// and cuts a reply tail and the announcement; it begins none, and announcements wait out the ring
func TestARingEndsTheSessionAndTheAnnouncement(t *testing.T) {
	var ringing atomic.Bool
	r := newRig(t, 0, WithRinging(ringing.Load))
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	r.next(t)
	r.handle(t, &cadencev2.StreamStart{StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	_, w := r.began(t)
	r.handle(t, announceStart("an1", ""))
	_, an := r.began(t)

	ringing.Store(true)
	r.triggers <- audio.Trigger{Mode: audio.Ring}
	if c := r.next(t).GetSessionCancel(); c.GetSessionId() != "s1" || c.GetReason() != "ring" {
		t.Fatalf("cancel = %v", c)
	}
	if !aborted(w, 2*time.Second) || !aborted(an, 2*time.Second) {
		t.Fatal("the ring left the reply or the announcement playing")
	}
	r.quiet(t, "after the ring began")
	r.handle(t, announceStart("an2", ""))
	r.noBegin(t, "an announcement during a ring")
	if !strings.Contains(r.logs.String(), "msg=stream_refused stream_id=an2 reason=ringing") {
		t.Fatalf("log: %s", r.logs)
	}

	// a tail that still plays when a ring begins is cut too
	ringing.Store(false)
	tail := endedReplyIn(t, r, "s2")
	<-r.speaker.began
	r.handle(t, &cadencev2.SessionClose{SessionId: "s2"})
	r.triggers <- audio.Trigger{Mode: audio.Ring}
	if !aborted(tail, 2*time.Second) {
		t.Fatal("the ring left the tail playing")
	}
	r.quiet(t, "a ring over a tail")
	r.handle(t, announceStart("an3", ""))
	if st, _ := r.began(t); st.GetStreamId() != "an3" {
		t.Fatalf("began %v", st)
	}
}

// an activation that reached the queue before a ring began, or races it, begins nothing while it rings; the switch and
// a ring still end what runs
func TestNoSessionBeginsWhileARingSounds(t *testing.T) {
	var ringing atomic.Bool
	ringing.Store(true)
	r := newRig(t, 0, WithRinging(ringing.Load))
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Fixture: "weather"}
	r.triggers <- audio.Trigger{Mode: audio.Mute}
	r.quiet(t, "activations during a ring")
	if n := strings.Count(r.logs.String(), "msg=activation_refused reason=ringing"); n != 3 {
		t.Fatalf("%d refusals: %s", n, r.logs)
	}
	ringing.Store(false)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	if s := r.next(t).GetSessionStart(); s.GetSessionId() != "s1" {
		t.Fatalf("start after the ring = %v", s)
	}
}
