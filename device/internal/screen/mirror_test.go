package screen_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// audiod as far as a stop of its session goes: each stop counted and answered with code
type stopFake struct {
	answer func(*audiodv1.Envelope)
	code   string
	mu     sync.Mutex
	stops  int
}

func (a *stopFake) send(env *audiodv1.Envelope) error {
	mc := env.GetMediaCommand()
	if mc == nil {
		return nil
	}
	a.mu.Lock()
	if mc.GetAction() == audiodv1.MediaAction_MEDIA_ACTION_STOP {
		a.stops++
	}
	code := a.code
	a.mu.Unlock()
	go a.answer(&audiodv1.Envelope{Body: &audiodv1.Envelope_CommandResult{CommandResult: &audiodv1.CommandResult{
		RequestId: mc.GetRequestId(), Ok: code == "", Code: code,
	}}})
	return nil
}

func (a *stopFake) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stops
}

func (a *stopFake) failWith(code string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.code = code
}

func askStop(t *testing.T, s *screentest.Screen, session, request string) string {
	t.Helper()
	if err := s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_MirrorStop{MirrorStop: &screenv1.MirrorStop{
		SessionId: session, RequestId: request,
	}}}); err != nil {
		t.Fatal(err)
	}
	return frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetCommandResult().GetRequestId() == request
	}).GetCommandResult().GetCode()
}

func audiodSession(r *rig, source audiodv1.MediaSourceKind) {
	r.media.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_MediaState{MediaState: &audiodv1.MediaState{
		SessionId: "ms-1", Source: source, State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING,
	}}})
}

// the screen's stop key on a mirror: the active session's stop reaches audiod as a stop of its screencast session and
// is answered; a stale id, no mirror, or a session that is no mirror stops nothing; the sender is never logged
func TestScreenStopsTheActiveMirror(t *testing.T) {
	logs := &lockedLog{}
	r := newRig(t, screen.Options{Log: slog.New(slog.NewTextHandler(logs, nil))})
	a := &stopFake{answer: r.media.MediaEnvelope}
	r.media.MediaReady(a.send)
	s := r.connect(t, screentest.Options{})

	if c := askStop(t, s, "airplay-1", "q0"); c != "no_session" {
		t.Fatalf("no mirror: %q", c)
	}
	r.srv.MirrorState(&audiodv1.MirrorState{SessionId: "airplay-1", Active: true, ClientName: "iPhone Маши"})
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMirrorStart().GetSessionId() == "airplay-1" })

	// the music took the speaker meanwhile: not the mirror's to stop
	audiodSession(r, audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_URL)
	if c := askStop(t, s, "airplay-1", "q1"); c != "no_session" {
		t.Fatalf("music: %q", c)
	}
	audiodSession(r, audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_SCREENCAST)
	if c := askStop(t, s, "airplay-0", "q2"); c != "no_session" {
		t.Fatalf("stale id: %q", c)
	}
	if c := askStop(t, s, "", "q3"); c != "no_session" {
		t.Fatalf("no id: %q", c)
	}
	if n := a.count(); n != 0 {
		t.Fatalf("stops = %d before the active id", n)
	}

	a.failWith("refused")
	if c := askStop(t, s, "airplay-1", "q4"); c != "refused" {
		t.Fatalf("refused: %q", c)
	}
	a.failWith("")
	if c := askStop(t, s, "airplay-1", "q5"); c != "" {
		t.Fatalf("stop: %q", c)
	}
	if n := a.count(); n != 2 {
		t.Fatalf("stops = %d", n)
	}
	// audiod reports the end as for any: the screen hears it with reason stopped
	r.srv.MirrorState(&audiodv1.MirrorState{SessionId: "airplay-1", Reason: "stopped"})
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMirrorStop().GetReason() == "stopped" })
	if c := askStop(t, s, "airplay-1", "q6"); c != "no_session" {
		t.Fatalf("after its end: %q", c)
	}
	deadline := time.Now().Add(wait)
	for !strings.Contains(logs.String(), "mirror_stop_requested") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if out := logs.String(); !strings.Contains(out, "mirror_stop_requested") || !strings.Contains(out, "airplay-1") ||
		strings.Contains(out, "iPhone") || strings.Contains(out, "Маши") {
		t.Fatalf("logs:\n%s", out)
	}
}
