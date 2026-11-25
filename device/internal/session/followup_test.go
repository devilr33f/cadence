package session

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const window = 5 * time.Second

func followUpClose(id string) *cadencev2.SessionClose {
	return &cadencev2.SessionClose{SessionId: id, FollowUpAllowed: true, FollowUpWindowMs: uint32(window / time.Millisecond)}
}

// heardReply plays a reply of frames frames in session id to its end, as the server streams one
func heardReply(t *testing.T, r *rig, id string, frames int) *fakeWriter {
	t.Helper()
	stream := "rp-" + id
	r.handle(t, &cadencev2.StreamStart{StreamId: stream, SessionId: id, Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY, Channels: 1})
	_, w := r.began(t)
	for i := range frames {
		r.handle(t, &cadencev2.AudioDown{StreamId: stream, Seq: uint32(i), Opus: []byte{byte(i + 1)}})
	}
	r.handle(t, &cadencev2.StreamEnd{StreamId: stream})
	if !ended(w, 2*time.Second) {
		t.Fatal("reply not ended")
	}
	return w
}

func (r *rig) woken(t *testing.T) {
	t.Helper()
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	if s := r.next(t).GetSessionStart(); s.GetTrigger() != cadencev2.Trigger_TRIGGER_WAKE {
		t.Fatalf("start = %v", s)
	}
}

func (r *rig) wait(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-r.waits:
		return d
	case <-time.After(2 * time.Second):
		t.Fatal("no wait armed")
		return 0
	}
}

// a reply heard whole opens a follow-up at once, without a wake: the microphone streams to a new session, and the
// reply's tail plays on under the follow-up and its own reply (M4a-R7); the music stays ducked throughout
func TestAHeardReplyOpensAFollowUp(t *testing.T) {
	recent := &Recent{}
	r := newRig(t, 0, WithRecent(recent))
	r.woken(t)
	tail := heardReply(t, r, "s1", 3)
	r.handle(t, followUpClose("s1"))
	s := r.next(t).GetSessionStart()
	if s.GetSessionId() != "s2" || s.GetTrigger() != cadencev2.Trigger_TRIGGER_FOLLOW_UP || s.GetWake() != nil {
		t.Fatalf("start = %v", s)
	}
	if got := r.wait(t); got != window {
		t.Fatalf("window armed %v", got)
	}
	if tr, ok := recent.Trigger("s2"); !ok || tr != cadencev2.Trigger_TRIGGER_FOLLOW_UP {
		t.Fatalf("recorded %v, %v", tr, ok)
	}
	r.mic.mu.Lock()
	opened := append([]audio.Trigger(nil), r.mic.opened...)
	r.mic.mu.Unlock()
	if len(opened) != 2 || opened[1].Kind != cadencev2.Trigger_TRIGGER_FOLLOW_UP || opened[1].Fixture != "" {
		t.Fatalf("mic opened %+v", opened)
	}
	if aborted(tail, 50*time.Millisecond) {
		t.Fatal("the follow-up cut the reply's tail")
	}

	r.handle(t, &cadencev2.SessionEvent{SessionId: "s2", Kind: cadencev2.SessionEventKind_SESSION_EVENT_KIND_SPEECH_STARTED})
	next := heardReply(t, r, "s2", 2)
	if aborted(tail, 50*time.Millisecond) || next.count() != 2 {
		t.Fatalf("the follow-up's reply: tail cut %v, %d frames", aborted(tail, 0), next.count())
	}
	r.handle(t, &cadencev2.SessionClose{SessionId: "s2"})
	r.waitDucks(t, true, false)
	out := r.logs.String()
	for _, want := range []string{"msg=session_end session_id=s1 code=ok", "msg=session_start session_id=s2 trigger=TRIGGER_FOLLOW_UP"} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in log: %s", want, out)
		}
	}
	if strings.Contains(out, "follow_up_refused") {
		t.Fatalf("log: %s", out)
	}
}

// on the sim the say file is the microphone (M6a-R4): a reply in a session test audio spoke in opens a follow-up, and
// the follow-up is test audio too, so a command word heard in it still never acts
func TestTestAudioOpensAFollowUp(t *testing.T) {
	clock := &stepClock{now: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	a := &Activity{now: clock.Now}
	r := newRig(t, 0, WithActivity(a))
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Fixture: "weather", Test: true}
	r.next(t)
	heardReply(t, r, "s1", 2)
	r.handle(t, followUpClose("s1"))
	if s := r.next(t).GetSessionStart(); s.GetSessionId() != "s2" || s.GetTrigger() != cadencev2.Trigger_TRIGGER_FOLLOW_UP {
		t.Fatalf("start = %v", s)
	}
	// the window is armed once the microphone opened
	r.wait(t)
	r.mic.mu.Lock()
	opened := append([]audio.Trigger(nil), r.mic.opened...)
	r.mic.mu.Unlock()
	if len(opened) != 2 || opened[1] != (audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_FOLLOW_UP, Test: true}) {
		t.Fatalf("mic opened %+v", opened)
	}
	waitSnapshot(t, a, Snapshot{Session: "s2", SessionInjected: true, Replied: true, ReplyInjected: true})
	if strings.Contains(r.logs.String(), "follow_up_refused") {
		t.Fatalf("log: %s", r.logs)
	}
}

// the server closes a follow-up that heard only the previous reply's echo with follow_up_echo: that reply's tail plays
// on as after a normal close (M4a-R7), whenever the echo's final came; any other failure cuts it
func TestAnEchoCloseKeepsTheTail(t *testing.T) {
	for _, tc := range []struct {
		code string
		cut  bool
	}{{"follow_up_echo", false}, {"stt_failed", true}} {
		t.Run(tc.code, func(t *testing.T) {
			r := newRig(t, 0)
			r.woken(t)
			tail := heardReply(t, r, "s1", 3)
			r.handle(t, followUpClose("s1"))
			if s := r.next(t).GetSessionStart(); s.GetTrigger() != cadencev2.Trigger_TRIGGER_FOLLOW_UP {
				t.Fatalf("start = %v", s)
			}
			r.handle(t, &cadencev2.SessionClose{SessionId: "s2", Code: tc.code})
			r.waitDucks(t, true, false)
			if got := aborted(tail, 100*time.Millisecond); got != tc.cut {
				t.Fatalf("tail cut %v, want %v", got, tc.cut)
			}
		})
	}
}

// silence for the window ends the follow-up with a bare cancel
func TestFollowUpSilenceCancels(t *testing.T) {
	r := newRig(t, 0)
	r.woken(t)
	heardReply(t, r, "s1", 1)
	r.handle(t, followUpClose("s1"))
	r.next(t)
	r.wait(t)
	r.clock <- time.Now()
	if c := r.next(t).GetSessionCancel(); c.GetSessionId() != "s2" || c.GetReason() != "follow_up_silence" {
		t.Fatalf("cancel = %v", c)
	}
	r.quiet(t, "after the silence")
}

// a wake during the window is ignored, as over any session, and a press cancels the follow-up
func TestWakeIgnoredAndPressCancelsTheFollowUp(t *testing.T) {
	r := newRig(t, 0)
	r.woken(t)
	heardReply(t, r, "s1", 1)
	r.handle(t, followUpClose("s1"))
	r.next(t)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	r.quiet(t, "a wake in the window")
	if !strings.Contains(r.logs.String(), "msg=wake_ignored session_id=s2 reason=active") {
		t.Fatalf("log: %s", r.logs)
	}
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	if c := r.next(t).GetSessionCancel(); c.GetSessionId() != "s2" || c.GetReason() != "local_cancel" {
		t.Fatalf("cancel = %v", c)
	}
}

// the server's leave is not enough: each fact of the device's own refuses the follow-up, and the reply's tail plays out
func TestEachFactRefusesTheFollowUp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		run    func(t *testing.T, r *rig, flags *facts)
	}{
		{"muted", "muted", func(t *testing.T, r *rig, f *facts) {
			r.woken(t)
			heardReply(t, r, "s1", 2)
			f.muted.Store(true)
			r.handle(t, followUpClose("s1"))
		}},
		{"ringing", "ringing", func(t *testing.T, r *rig, f *facts) {
			r.woken(t)
			heardReply(t, r, "s1", 2)
			f.ringing.Store(true)
			r.handle(t, followUpClose("s1"))
		}},
		{"no reply", "not_heard", func(t *testing.T, r *rig, _ *facts) {
			r.woken(t)
			r.handle(t, followUpClose("s1"))
		}},
		{"reply still streaming", "not_heard", func(t *testing.T, r *rig, _ *facts) {
			r.woken(t)
			r.handle(t, &cadencev2.StreamStart{StreamId: "rp", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
			r.began(t)
			r.handle(t, &cadencev2.AudioDown{StreamId: "rp", Opus: []byte{1}})
			r.handle(t, followUpClose("s1"))
		}},
		{"reply frames lost", "not_heard", func(t *testing.T, r *rig, _ *facts) {
			r.woken(t)
			heardReply(t, r, "s1", 1)
			r.speaker.mu.Lock()
			r.speaker.failWrites = true
			r.speaker.mu.Unlock()
			r.handle(t, &cadencev2.StreamStart{StreamId: "rp2", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
			r.began(t)
			r.handle(t, &cadencev2.AudioDown{StreamId: "rp2", Opus: []byte{1}})
			r.handle(t, &cadencev2.StreamEnd{StreamId: "rp2"})
			r.handle(t, followUpClose("s1"))
		}},
		{"error close", "error", func(t *testing.T, r *rig, _ *facts) {
			r.woken(t)
			heardReply(t, r, "s1", 2)
			c := followUpClose("s1")
			c.Code = "tts_failed"
			r.handle(t, c)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &facts{}
			r := newRig(t, 0, WithMuted(f.muted.Load), WithRinging(f.ringing.Load))
			tc.run(t, r, f)
			r.quiet(t, "a refused follow-up")
			if !strings.Contains(r.logs.String(), "msg=follow_up_refused session_id=s1 reason="+tc.reason) {
				t.Fatalf("log: %s", r.logs)
			}
			if !strings.Contains(r.logs.String(), "msg=session_end session_id=s1") {
				t.Fatalf("session not ended: %s", r.logs)
			}
		})
	}
}

type facts struct{ muted, ringing atomic.Bool }

// a close without the server's leave, or with no window, opens nothing and logs no refusal
func TestNoFollowUpWithoutTheServersLeave(t *testing.T) {
	for _, sc := range []*cadencev2.SessionClose{
		{SessionId: "s1"},
		{SessionId: "s1", FollowUpAllowed: true},
		{SessionId: "s1", FollowUpWindowMs: 5000},
	} {
		r := newRig(t, 0)
		r.woken(t)
		tail := heardReply(t, r, "s1", 2)
		r.handle(t, sc)
		r.quiet(t, "a close with no follow-up")
		if strings.Contains(r.logs.String(), "follow_up_refused") || aborted(tail, 0) {
			t.Fatalf("close %v: tail cut %v, log: %s", sc, aborted(tail, 0), r.logs)
		}
	}
}

// a close for a session that no longer runs opens nothing: a superseded one, or one a follow-up already followed
func TestAStaleCloseOpensNothing(t *testing.T) {
	r := newRig(t, 0)
	r.woken(t)
	heardReply(t, r, "s1", 2)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	if s := r.next(t).GetSessionStart(); s.GetSessionId() != "s2" {
		t.Fatalf("start = %v", s)
	}
	r.handle(t, followUpClose("s1"))
	r.quiet(t, "a close of a superseded session")

	heardReply(t, r, "s2", 2)
	r.handle(t, followUpClose("s2"))
	if s := r.next(t).GetSessionStart(); s.GetSessionId() != "s3" || s.GetTrigger() != cadencev2.Trigger_TRIGGER_FOLLOW_UP {
		t.Fatalf("start = %v", s)
	}
	r.handle(t, followUpClose("s2"))
	r.quiet(t, "the close again")
	r.handle(t, &cadencev2.SessionClose{SessionId: "s3"})
	r.handle(t, followUpClose("s3"))
	r.quiet(t, "a close after the session ended")
}

// a command word that lowered the level ends the session its microphone streamed to, if that one still runs
func TestACommandWordEndsTheSessionItNames(t *testing.T) {
	r := newRig(t, 0)
	r.triggers <- audio.Trigger{Mode: audio.CommandEnd, Session: "s1"}
	r.quiet(t, "a command end with no session")
	r.woken(t)
	w := heardReply(t, r, "s1", 2)
	r.triggers <- audio.Trigger{Mode: audio.CommandEnd, Session: "s0"}
	r.quiet(t, "a command end for another session")
	r.triggers <- audio.Trigger{Mode: audio.CommandEnd, Session: "s1"}
	if c := r.next(t).GetSessionCancel(); c.GetSessionId() != "s1" || c.GetReason() != "command_volume" {
		t.Fatalf("cancel = %v", c)
	}
	if !aborted(w, 2*time.Second) {
		t.Fatal("the reply played on")
	}
}

// the level in force goes back on the hardware before each reply stream begins, never before an announcement
func TestTheLevelGoesBackBeforeEachReply(t *testing.T) {
	var checks atomic.Int32
	var r *rig
	var begunFirst atomic.Bool
	r = newRig(t, 0, WithBeforeReply(func() {
		checks.Add(1)
		if len(r.speaker.began) > 0 {
			begunFirst.Store(true)
		}
	}))
	r.handle(t, announceStart("an1", ""))
	r.began(t)
	if checks.Load() != 0 {
		t.Fatal("rechecked before an announcement")
	}
	r.woken(t)
	heardReply(t, r, "s1", 1)
	heardReply(t, r, "s1", 1)
	if n := checks.Load(); n != 2 || begunFirst.Load() {
		t.Fatalf("%d checks, a stream begun first: %v", n, begunFirst.Load())
	}
}

// a follow-up whose microphone fails keeps the reply's tail, as the echo close does: nothing of it was cut by a user
func TestAFollowUpMicFailureKeepsTheTail(t *testing.T) {
	r := newRig(t, 0)
	r.woken(t)
	tail := heardReply(t, r, "s1", 3)
	r.mic.mu.Lock()
	r.mic.failing = true
	r.mic.mu.Unlock()
	r.handle(t, followUpClose("s1"))
	r.next(t)
	if c := r.next(t).GetSessionCancel(); c.GetReason() != "mic_failed" {
		t.Fatalf("cancel = %v", c)
	}
	if aborted(tail, 100*time.Millisecond) {
		t.Fatal("a failed follow-up cut the reply's tail")
	}
}

// the window the server sends is clamped to its own bound
func TestAnOversizedFollowUpWindowIsClamped(t *testing.T) {
	r := newRig(t, 0)
	r.woken(t)
	heardReply(t, r, "s1", 3)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1", FollowUpAllowed: true, FollowUpWindowMs: 600000})
	r.next(t)
	if got := r.wait(t); got != 15*time.Second {
		t.Fatalf("window armed %v", got)
	}
}
