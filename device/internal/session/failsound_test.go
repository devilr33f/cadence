package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func TestFailurePrompt(t *testing.T) {
	for code, want := range map[string]string{
		"send_failed": PromptOffline,
		"mic_failed":  PromptFailed,
		"stt_failed":  PromptNotHeard, "stt_unavailable": PromptNotHeard,
		"tts_failed": PromptFailed, "llm_failed": PromptFailed, "provider_error": PromptFailed, "rate_limited": PromptFailed,
		"auth": PromptFailed, "timeout": PromptFailed, "internal": PromptFailed, "not_configured": PromptFailed,
		"ok": "", "no_speech": "", "follow_up_silence": "", "follow_up_echo": "", "stopped": "", "cancelled": "",
		"superseded": "", "command_volume": "", "local_cancel": "", "muted": "", "ring": "", "shutdown": "", "": "",
	} {
		if got := FailurePrompt(code); got != want {
			t.Errorf("FailurePrompt(%q) = %q, want %q", code, got, want)
		}
	}
}

type soundCall struct{ id, lang string }

type fakeSounder struct {
	mu    sync.Mutex
	calls []soundCall
}

func (f *fakeSounder) Begin(*cadencev2.StreamStart) (audio.StreamWriter, error) {
	return nil, errors.New("none")
}

func (f *fakeSounder) ErrorSound(id, lang string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, soundCall{id, lang})
}

func (f *fakeSounder) played() []soundCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]soundCall(nil), f.calls...)
}

type fakeErrorLights struct{ offline, errs atomic.Int32 }

func (l *fakeErrorLights) Offline() { l.offline.Add(1) }
func (l *fakeErrorLights) Error()   { l.errs.Add(1) }

func newSounds(t *testing.T) (*ErrorSounds, *fakeSounder, *fakeErrorLights) {
	t.Helper()
	e := NewErrorSounds(slog.New(slog.NewTextHandler(io.Discard, nil)))
	sp, l := &fakeSounder{}, &fakeErrorLights{}
	e.UseSpeaker(sp)
	e.UseLights(l)
	return e, sp, l
}

func TestErrorSoundsFollowTheSetting(t *testing.T) {
	e, sp, l := newSounds(t)
	e.Play(PromptFailed)
	e.Set(ErrorSoundPrompt, "ru")
	e.Play(PromptNotHeard)
	e.Set(ErrorSoundBeep, "ru")
	e.Play(PromptFailed)
	e.Set(ErrorSoundOff, "ru")
	e.Play(PromptFailed)
	e.Set("loud", "")
	e.Play("")
	e.Play(PromptFailed)
	want := []soundCall{{PromptFailed, DefaultLanguage}, {PromptNotHeard, "ru"}, {"", ""}, {PromptFailed, DefaultLanguage}}
	if got := sp.played(); len(got) != len(want) {
		t.Fatalf("played %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("played %v, want %v", got, want)
			}
		}
	}
	// the lights pulse whatever the sound, off included
	if n := l.errs.Load(); n != 5 {
		t.Fatalf("error pulses = %d, want 5", n)
	}
}

func TestOfflinePromptAtMostEveryTenSecondsBlinksEveryTime(t *testing.T) {
	e, sp, l := newSounds(t)
	now := time.Unix(1000, 0)
	e.now = func() time.Time { return now }
	e.Play(PromptOffline)
	now = now.Add(9 * time.Second)
	e.Play(PromptOffline)
	now = now.Add(2 * time.Second)
	e.Play(PromptOffline)
	if got := sp.played(); len(got) != 2 || got[0].id != PromptOffline || got[1].id != PromptOffline {
		t.Fatalf("played %v", got)
	}
	if n := l.offline.Load(); n != 3 {
		t.Fatalf("offline blinks = %d, want 3", n)
	}
}

func TestNoSpeakerPlaysNothing(_ *testing.T) {
	e := NewErrorSounds(slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.UseSpeaker(&fakeSpeaker{})
	e.Play(PromptFailed)
	var none *ErrorSounds
	none.Play(PromptFailed)
}

func waitPlayed(t *testing.T, sp *fakeSounder, n int) []soundCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := sp.played(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return sp.played()
}

func TestServerFailureTellsTheOwnerOnce(t *testing.T) {
	e, sp, _ := newSounds(t)
	r := newRig(t, 0, WithErrorSounds(e))
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1", Code: "stt_failed"})
	got := waitPlayed(t, sp, 1)
	if len(got) != 1 || got[0].id != PromptNotHeard {
		t.Fatalf("played %v", got)
	}
	// a normal close says nothing
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s2"})
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s3", Code: "llm_failed"})
	got = waitPlayed(t, sp, 2)
	if len(got) != 2 || got[1].id != PromptFailed {
		t.Fatalf("played %v", got)
	}
}

func TestTestAudioAndRingsTellNoFailure(t *testing.T) {
	e, sp, _ := newSounds(t)
	var ringing atomic.Bool
	r := newRig(t, 0, WithErrorSounds(e), WithRinging(ringing.Load))
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Test: true}
	r.next(t)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1", Code: "stt_failed"})
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	ringing.Store(true)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s2", Code: "tts_failed"})
	for deadline := time.Now().Add(2 * time.Second); !strings.Contains(r.logs.String(), "session_id=s2 code=tts_failed"); {
		if time.Now().After(deadline) {
			t.Fatal("s2 never ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
	ringing.Store(false)
	// a later failure still plays: the two above were dropped, not queued
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	r.next(t)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s3", Code: "internal"})
	waitPlayed(t, sp, 1)
	time.Sleep(50 * time.Millisecond)
	if got := sp.played(); len(got) != 1 || got[0].id != PromptFailed {
		t.Fatalf("played %v", got)
	}
}

func TestMicFailureSaysFailed(t *testing.T) {
	e, sp, _ := newSounds(t)
	r := newRig(t, 0, WithErrorSounds(e))
	r.mic.failing = true
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	if c := r.next(t).GetSessionCancel(); c.GetReason() != "mic_failed" {
		t.Fatalf("cancel = %v", c)
	}
	if got := waitPlayed(t, sp, 1); len(got) != 1 || got[0].id != PromptFailed {
		t.Fatalf("played %v", got)
	}
}

func TestSessionStartUnsentSaysOffline(t *testing.T) {
	e, sp, l := newSounds(t)
	triggers := make(chan audio.Trigger, 1)
	c := New(audio.IO{Triggers: triggers, Mic: &fakeMic{}, Speaker: &fakeSpeaker{}},
		func(*cadencev2.Envelope) error { return errors.New("link down") }, func() string { return "s1" },
		slog.New(slog.NewTextHandler(io.Discard, nil)), WithErrorSounds(e))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	if got := waitPlayed(t, sp, 1); len(got) != 1 || got[0].id != PromptOffline {
		t.Fatalf("played %v", got)
	}
	if l.offline.Load() != 1 {
		t.Fatalf("offline blinks = %d", l.offline.Load())
	}
}
