package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type fakeMic struct {
	failing bool
	failed  <-chan []byte
	mu      sync.Mutex
	packets int
	// the first packet waits for hold to close, as audiod's do during a vendor probe
	hold <-chan struct{}
	// the channel closes, as a normal stop, after the packets
	ends   bool
	opened []audio.Trigger
	chans  []chan []byte
}

func (m *fakeMic) Open(ctx context.Context, t audio.Trigger) (<-chan []byte, error) {
	m.mu.Lock()
	m.opened = append(m.opened, t)
	n, hold, ends := m.packets, m.hold, m.ends
	ch := make(chan []byte)
	m.chans = append(m.chans, ch)
	if m.failing {
		m.failed = ch
		close(ch)
		m.mu.Unlock()
		return ch, nil
	}
	m.mu.Unlock()
	go func() {
		defer close(ch)
		if hold != nil {
			select {
			case <-hold:
			case <-ctx.Done():
				return
			}
		}
		if ends && n == 0 {
			return
		}
		for i := range n {
			select {
			case ch <- []byte{byte(i + 1)}:
			case <-ctx.Done():
				return
			}
		}
		<-ctx.Done()
	}()
	return ch, nil
}

type fakeWriter struct {
	mu        sync.Mutex
	packets   [][]byte
	ended     chan struct{}
	aborted   chan struct{}
	abortOnce sync.Once
	// every write fails, as one to an audiod that went away
	fails bool
}

var errWriteFailed = errors.New("write failed")

func (w *fakeWriter) Write(_ uint32, p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fails {
		return errWriteFailed
	}
	w.packets = append(w.packets, p)
	return nil
}

func (w *fakeWriter) End() error { close(w.ended); return nil }

func (w *fakeWriter) Abort() { w.abortOnce.Do(func() { close(w.aborted) }) }

type fakeSpeaker struct {
	began   chan *cadencev2.StreamStart
	writers chan *fakeWriter

	mu      sync.Mutex
	ducks   []bool
	earcons int
	// the writers it begins fail every write
	failWrites bool
}

func (s *fakeSpeaker) Duck(active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ducks = append(s.ducks, active)
}

func (s *fakeSpeaker) WakeEarcon() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.earcons++
}

func (s *fakeSpeaker) earconCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.earcons
}

func (s *fakeSpeaker) ducked() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.ducks...)
}

func (s *fakeSpeaker) Begin(st *cadencev2.StreamStart) (audio.StreamWriter, error) {
	s.mu.Lock()
	fails := s.failWrites
	s.mu.Unlock()
	w := &fakeWriter{ended: make(chan struct{}), aborted: make(chan struct{}), fails: fails}
	s.began <- st
	s.writers <- w
	return w, nil
}

type rig struct {
	c        *Controller
	triggers chan audio.Trigger
	sent     chan *cadencev2.Envelope
	mic      *fakeMic
	speaker  *fakeSpeaker
	clock    chan time.Time
	cancel   context.CancelFunc
	done     chan struct{}
	logs     *syncBuffer
	// runs in each send, before the frame is queued; set it before the first trigger
	onSend func(*cadencev2.Envelope)
	// each wait the controller arms, in order
	waits chan time.Duration
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newRig(t *testing.T, micPackets int, opts ...Option) *rig {
	t.Helper()
	r := &rig{
		triggers: make(chan audio.Trigger, 4),
		sent:     make(chan *cadencev2.Envelope, 256),
		mic:      &fakeMic{packets: micPackets},
		speaker:  &fakeSpeaker{began: make(chan *cadencev2.StreamStart, 4), writers: make(chan *fakeWriter, 4)},
		clock:    make(chan time.Time, 1),
		waits:    make(chan time.Duration, 16),
		done:     make(chan struct{}),
		logs:     &syncBuffer{},
	}
	n := 0
	ids := []string{"s1", "s2", "s3", "s4", "s5"}
	r.c = New(audio.IO{Triggers: r.triggers, Mic: r.mic, Speaker: r.speaker},
		func(env *cadencev2.Envelope) error {
			if r.onSend != nil {
				r.onSend(env)
			}
			r.sent <- env
			return nil
		},
		func() string { n++; return ids[n-1] },
		slog.New(slog.NewTextHandler(r.logs, nil)), opts...)
	r.c.after = func(d time.Duration) <-chan time.Time {
		select {
		case r.waits <- d:
		default:
		}
		return r.clock
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() { defer close(r.done); r.c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-r.done })
	return r
}

func (r *rig) next(t *testing.T) *cadencev2.Envelope {
	t.Helper()
	select {
	case env := <-r.sent:
		return env
	case <-time.After(2 * time.Second):
		t.Fatal("no frame sent")
		return nil
	}
}

func (r *rig) handle(t *testing.T, body any) {
	t.Helper()
	env := &cadencev2.Envelope{}
	switch b := body.(type) {
	case *cadencev2.SessionEvent:
		env.Body = &cadencev2.Envelope_SessionEvent{SessionEvent: b}
	case *cadencev2.StreamStart:
		env.Body = &cadencev2.Envelope_StreamStart{StreamStart: b}
	case *cadencev2.AudioDown:
		env.Body = &cadencev2.Envelope_AudioDown{AudioDown: b}
	case *cadencev2.StreamEnd:
		env.Body = &cadencev2.Envelope_StreamEnd{StreamEnd: b}
	case *cadencev2.SessionClose:
		env.Body = &cadencev2.Envelope_SessionClose{SessionClose: b}
	}
	if !r.c.Handle(env) {
		t.Fatalf("%T not handled", body)
	}
}

func TestSessionStreamsMic(t *testing.T) {
	r := newRig(t, 30)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Fixture: "what-time"}
	start := r.next(t).GetSessionStart()
	if start.GetSessionId() != "s1" || start.GetTrigger() != cadencev2.Trigger_TRIGGER_WAKE {
		t.Fatalf("start = %v", start)
	}
	for i := range 30 {
		m := r.next(t).GetMicAudio()
		if m == nil || m.GetSessionId() != "s1" || m.GetSeq() != uint32(i) || len(m.GetOpus()) != 1 {
			t.Fatalf("packet %d = %v", i, m)
		}
	}
	if got := r.mic.opened[0].Fixture; got != "what-time" {
		t.Fatalf("mic fixture = %q", got)
	}
}

func TestReplyWrittenToSpeaker(t *testing.T) {
	r := newRig(t, 0)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	r.next(t)
	r.handle(t, &cadencev2.SessionEvent{SessionId: "s1", Kind: cadencev2.SessionEventKind_SESSION_EVENT_KIND_SPEECH_STARTED})
	r.handle(t, &cadencev2.StreamStart{StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY, Codec: cadencev2.Codec_CODEC_OPUS, Channels: 1})
	for i := range 3 {
		r.handle(t, &cadencev2.AudioDown{StreamId: "st1", Seq: uint32(i), Opus: []byte{byte(i)}})
	}
	r.handle(t, &cadencev2.StreamEnd{StreamId: "st1"})
	w := <-r.speaker.writers
	select {
	case <-w.ended:
	case <-time.After(2 * time.Second):
		t.Fatal("writer not ended")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.packets) != 3 {
		t.Fatalf("packets = %d", len(w.packets))
	}
}

func TestNoSpeechCancels(t *testing.T) {
	r := newRig(t, 1)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	r.next(t)
	r.clock <- time.Now()
	c := r.next(t).GetSessionCancel()
	if c == nil || c.GetSessionId() != "s1" || c.GetReason() != "no_speech" {
		t.Fatalf("cancel = %v", c)
	}
	// the session is over: later frames for it are stale
	r.handle(t, &cadencev2.StreamStart{StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	select {
	case <-r.speaker.began:
		t.Fatal("stream of an ended session began")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSpeechStartedDisarmsNoSpeech(t *testing.T) {
	r := newRig(t, 1)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	r.next(t)
	r.handle(t, &cadencev2.SessionEvent{SessionId: "s1", Kind: cadencev2.SessionEventKind_SESSION_EVENT_KIND_SPEECH_STARTED})
	// the stream start is a barrier: the loop took the event before it
	r.handle(t, &cadencev2.StreamStart{StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	<-r.speaker.began
	r.clock <- time.Now()
	select {
	case env := <-r.sent:
		t.Fatalf("unexpected frame %v", env)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTriggerSupersedes(t *testing.T) {
	r := newRig(t, 0)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	start := r.next(t).GetSessionStart()
	if start.GetSessionId() != "s2" || start.GetTrigger() != cadencev2.Trigger_TRIGGER_BUTTON {
		t.Fatalf("start = %v", start)
	}
	// a frame of the superseded session is stale
	r.handle(t, &cadencev2.StreamStart{StreamId: "old", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	r.handle(t, &cadencev2.StreamStart{StreamId: "new", SessionId: "s2", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	if st := <-r.speaker.began; st.GetStreamId() != "new" {
		t.Fatalf("began %q", st.GetStreamId())
	}
}

func TestSessionCloseEndsSession(t *testing.T) {
	r := newRig(t, 0)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1", Code: "stt_failed"})
	r.handle(t, &cadencev2.StreamStart{StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	select {
	case env := <-r.sent:
		t.Fatalf("unexpected frame %v", env)
	case <-r.speaker.began:
		t.Fatal("stream of a closed session began")
	case <-time.After(50 * time.Millisecond):
	}
	// a new session starts afterwards
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	if id := r.next(t).GetSessionStart().GetSessionId(); id != "s2" {
		t.Fatalf("id = %q", id)
	}
}

func (m *fakeMic) MicFailure(p <-chan []byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failed != nil && m.failed == p
}

// a supersede cuts a reply that is still playing and starts the next session
// at once, without waiting for it to finish
func TestSupersedeAbortsLongReply(t *testing.T) {
	r := newRig(t, 0)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	r.handle(t, &cadencev2.StreamStart{StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	w := <-r.speaker.writers
	r.handle(t, &cadencev2.StreamEnd{StreamId: "st1"})
	<-w.ended
	r.handle(t, &cadencev2.StreamStart{StreamId: "st2", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	w2 := <-r.speaker.writers
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	if id := r.next(t).GetSessionStart().GetSessionId(); id != "s2" {
		t.Fatalf("id = %q", id)
	}
	select {
	case <-w2.aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("open stream not aborted")
	}
}

func TestMicFailureEndsSession(t *testing.T) {
	r := newRig(t, 0)
	r.mic.failing = true
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	c := r.next(t).GetSessionCancel()
	if c == nil || c.GetSessionId() != "s1" || c.GetReason() != "mic_failed" {
		t.Fatalf("cancel = %v", c)
	}
}

func aborted(w *fakeWriter, wait time.Duration) bool {
	select {
	case <-w.aborted:
		return true
	case <-time.After(wait):
		return false
	}
}

func endedReply(t *testing.T, r *rig) *fakeWriter {
	t.Helper()
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	r.handle(t, &cadencev2.StreamStart{StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	w := <-r.speaker.writers
	r.handle(t, &cadencev2.StreamEnd{StreamId: "st1"})
	<-w.ended
	return w
}

// the reply's StreamEnd came but audiod still plays its tail
func TestSupersedeCutsEndedReply(t *testing.T) {
	r := newRig(t, 0)
	w := endedReply(t, r)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	r.next(t)
	if !aborted(w, 2*time.Second) {
		t.Fatal("ended reply not cut by the supersede")
	}
}

func TestNormalCloseLetsTailPlayUntilNextSession(t *testing.T) {
	r := newRig(t, 0)
	w := endedReply(t, r)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1"})
	if aborted(w, 100*time.Millisecond) {
		t.Fatal("normal close cut the tail")
	}
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	if !aborted(w, 2*time.Second) {
		t.Fatal("next session did not cut the old tail")
	}
}

func TestCancelCutsEndedReply(t *testing.T) {
	r := newRig(t, 0)
	w := endedReply(t, r)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1", Code: "stt_failed"})
	if !aborted(w, 2*time.Second) {
		t.Fatal("failed close left the tail playing")
	}
}

func (r *rig) waitDucks(t *testing.T, want ...bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := r.speaker.ducked()
		if fmt.Sprint(got) == fmt.Sprint(want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ducks = %v, want %v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// the music ducks from a session's start to its end, whatever ends it
func TestMusicDuckedWhileSessionOpen(t *testing.T) {
	r := newRig(t, 0)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	r.waitDucks(t, true)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1"})
	r.waitDucks(t, true, false)

	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	r.next(t)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	if id := r.next(t).GetSessionStart().GetSessionId(); id != "s3" {
		t.Fatalf("id = %q", id)
	}
	r.waitDucks(t, true, false, true, false, true)
	r.cancel()
	<-r.done
	r.waitDucks(t, true, false, true, false, true, false)
}

func announceStart(id, session string) *cadencev2.StreamStart {
	return &cadencev2.StreamStart{StreamId: id, SessionId: session, Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_ANNOUNCE, Codec: cadencev2.Codec_CODEC_OPUS, Channels: 1, Duck: true}
}

func (r *rig) began(t *testing.T) (*cadencev2.StreamStart, *fakeWriter) {
	t.Helper()
	select {
	case st := <-r.speaker.began:
		return st, <-r.speaker.writers
	case <-time.After(2 * time.Second):
		t.Fatal("no stream began")
		return nil, nil
	}
}

func (r *rig) noBegin(t *testing.T, what string) {
	t.Helper()
	select {
	case st := <-r.speaker.began:
		t.Fatalf("%s began %q", what, st.GetStreamId())
	case <-time.After(50 * time.Millisecond):
	}
}

func (w *fakeWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.packets)
}

func ended(w *fakeWriter, wait time.Duration) bool {
	select {
	case <-w.ended:
		return true
	case <-time.After(wait):
		return false
	}
}

// an announcement plays with no session open, and its end lets the tail play
func TestAnnouncePlaysOutsideSession(t *testing.T) {
	r := newRig(t, 0)
	r.handle(t, announceStart("an1", ""))
	st, w := r.began(t)
	if st.GetStreamId() != "an1" || st.GetPurpose() != cadencev2.StreamPurpose_STREAM_PURPOSE_ANNOUNCE {
		t.Fatalf("began %v", st)
	}
	for i := range 3 {
		r.handle(t, &cadencev2.AudioDown{StreamId: "an1", Seq: uint32(i), Opus: []byte{byte(i)}})
	}
	r.handle(t, &cadencev2.StreamEnd{StreamId: "an1"})
	if !ended(w, 2*time.Second) {
		t.Fatal("announce not ended")
	}
	if n := w.count(); n != 3 {
		t.Fatalf("packets = %d", n)
	}
	if aborted(w, 50*time.Millisecond) {
		t.Fatal("ended announce cut")
	}
	select {
	case env := <-r.sent:
		t.Fatalf("unexpected frame %v", env)
	default:
	}
}

// a session that begins and ends, normally or not, leaves the announcement alone
func TestSessionLeavesAnnounce(t *testing.T) {
	r := newRig(t, 0)
	r.handle(t, announceStart("an1", ""))
	_, w := r.began(t)
	r.handle(t, &cadencev2.AudioDown{StreamId: "an1", Seq: 0, Opus: []byte{1}})
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	// still routed while the session is open
	r.handle(t, &cadencev2.AudioDown{StreamId: "an1", Seq: 1, Opus: []byte{2}})
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1", Code: "stt_failed"})
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	r.next(t)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s2"})
	r.handle(t, &cadencev2.StreamEnd{StreamId: "an1"})
	if !ended(w, 2*time.Second) {
		t.Fatal("announce not ended")
	}
	if aborted(w, 50*time.Millisecond) {
		t.Fatal("a session cut the announce")
	}
	if n := w.count(); n != 2 {
		t.Fatalf("packets = %d", n)
	}
}

func TestNewerAnnounceAbortsOlder(t *testing.T) {
	r := newRig(t, 0)
	r.handle(t, announceStart("an1", ""))
	_, w1 := r.began(t)
	r.handle(t, announceStart("an2", ""))
	st, w2 := r.began(t)
	if st.GetStreamId() != "an2" {
		t.Fatalf("began %q", st.GetStreamId())
	}
	if !aborted(w1, 2*time.Second) {
		t.Fatal("older announce not cut")
	}
	// frames of the cut one are stale
	r.handle(t, &cadencev2.AudioDown{StreamId: "an1", Seq: 0, Opus: []byte{1}})
	r.handle(t, &cadencev2.AudioDown{StreamId: "an2", Seq: 0, Opus: []byte{2}})
	r.handle(t, &cadencev2.StreamEnd{StreamId: "an2"})
	if !ended(w2, 2*time.Second) {
		t.Fatal("newer announce not ended")
	}
	if w1.count() != 0 || w2.count() != 1 {
		t.Fatalf("packets %d / %d", w1.count(), w2.count())
	}
	// one that ended and still plays its tail is cut too
	r.handle(t, announceStart("an3", ""))
	r.began(t)
	if !aborted(w2, 2*time.Second) {
		t.Fatal("ended announce not cut by a newer one")
	}
}

func TestStreamsWithoutTheirSessionRefused(t *testing.T) {
	r := newRig(t, 0)
	r.handle(t, announceStart("an1", "s1"))
	r.handle(t, &cadencev2.StreamStart{StreamId: "rp1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	r.noBegin(t, "a stream with no session")
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	// an announcement names no session, not even the open one
	r.handle(t, announceStart("an2", "s1"))
	r.noBegin(t, "an announce with a session id")
	r.handle(t, &cadencev2.StreamStart{StreamId: "md1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_MEDIA})
	r.noBegin(t, "a media stream")
	for id, reason := range map[string]string{"an1": "session", "rp1": "session", "an2": "session", "md1": "purpose"} {
		if !strings.Contains(r.logs.String(), "msg=stream_refused stream_id="+id+" reason="+reason) {
			t.Fatalf("no refusal of %s logged:\n%s", id, r.logs.String())
		}
	}
}

func TestShutdownAbortsAnnounce(t *testing.T) {
	r := newRig(t, 0)
	r.handle(t, announceStart("an1", ""))
	_, w := r.began(t)
	r.cancel()
	<-r.done
	if !aborted(w, 2*time.Second) {
		t.Fatal("shutdown left the announce playing")
	}
}

// regression: v2 never played the wake earcon. a wake word and a press play it once as their session opens; test
// audio does not
func TestWakeEarconAtAnOwnersSession(t *testing.T) {
	r := newRig(t, 0)
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE}
	r.next(t)
	r.waitEarcons(t, 1)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1"})
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	r.next(t)
	r.waitEarcons(t, 2)
	r.handle(t, &cadencev2.SessionClose{SessionId: "s2"})
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Test: true}
	r.next(t)
	// the controller's mic open follows the earcon's place: once it ran, a test session's earcon would have too
	time.Sleep(50 * time.Millisecond)
	if n := r.speaker.earconCount(); n != 2 {
		t.Fatalf("test audio played the earcon: %d", n)
	}
}

func (r *rig) waitEarcons(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for r.speaker.earconCount() != want {
		if time.Now().After(deadline) {
			t.Fatalf("earcons = %d, want %d", r.speaker.earconCount(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
