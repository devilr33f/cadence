package audiod

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const (
	doneGrace     = 2 * time.Second
	frameDuration = 20 * time.Millisecond
	micInbox      = 64
	// audiod's bound on AudioDown.opus; a longer packet makes it drop the client
	maxOpusBytes = 4000
)

// audiod names files after stream ids; one its bounds cannot take makes it
// drop the client
var streamID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var errBadStreamID = errors.New("audiod: stream id is not 1-64 of [A-Za-z0-9_-]")
var errOpusTooLarge = errors.New("audiod: opus packet over 4000 bytes")
var errStreamClosed = errors.New("audiod: stream closed")

type audioIO struct {
	c   *Client
	log *slog.Logger
	// how long a session waits for its first mic packet, and how long while vendorPending
	firstWait, pendingWait time.Duration
	// audiod has not yet said how its vendor engines settled; a capture waits for them
	vendorPending atomic.Bool

	mu      sync.Mutex
	nextMic uint64
	mics    map[string]*micSession
	streams map[string]chan struct{}
	// the packets channel of the last mic session that ended in failure
	failedMic <-chan []byte
}

// newIO serves the envelopes that arrive on in, which the caller forwards from
// c.Recv(), until in closes.
func newIO(c *Client, in <-chan *audiodv1.Envelope, firstWait, pendingWait time.Duration) *audioIO {
	a := &audioIO{
		c:           c,
		log:         c.log,
		firstWait:   firstWait,
		pendingWait: pendingWait,
		mics:        map[string]*micSession{},
		streams:     map[string]chan struct{}{},
	}
	go a.dispatch(in)
	return a
}

func (a *audioIO) dispatch(in <-chan *audiodv1.Envelope) {
	for env := range in {
		switch b := env.GetBody().(type) {
		case *audiodv1.Envelope_MicOpus:
			if m := a.mic(b.MicOpus.GetSessionId()); m != nil {
				m.push(micEvent{opus: b.MicOpus.GetOpus()})
			}
		case *audiodv1.Envelope_CaptureFailed:
			if m := a.mic(b.CaptureFailed.GetSessionId()); m != nil {
				m.push(micEvent{failed: true})
			}
		case *audiodv1.Envelope_StreamDone:
			tracing.Release(b.StreamDone.GetStreamId(), "ok", attribute.Int64("audiod.played_ms", int64(b.StreamDone.GetPlayedMs())))
			a.mu.Lock()
			ch := a.streams[b.StreamDone.GetStreamId()]
			delete(a.streams, b.StreamDone.GetStreamId())
			a.mu.Unlock()
			if ch != nil {
				close(ch)
			}
		}
	}
}

func (a *audioIO) mic(id string) *micSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mics[id]
}

type micEvent struct {
	opus   []byte
	failed bool
}

type micSession struct {
	id    string
	inbox chan micEvent
}

// push never blocks the dispatcher: a consumer that stalls loses packets.
func (m *micSession) push(e micEvent) {
	select {
	case m.inbox <- e:
	default:
	}
}

// Open starts a capture session; the channel closes when ctx ends, the
// capture fails, or no first packet comes in time. The capture is a span,
// voice.capture, under ctx's (the turn): audiod's marks of it land there.
func (a *audioIO) Open(ctx context.Context, t audio.Trigger) (<-chan []byte, error) {
	a.mu.Lock()
	a.nextMic++
	m := &micSession{id: fmt.Sprintf("mic-%d", a.nextMic), inbox: make(chan micEvent, micInbox)}
	a.mics[m.id] = m
	a.mu.Unlock()
	sctx, span := tracing.Start(ctx, "voice.capture", attribute.String(tracing.AttrSessionID, m.id))
	tracing.Bind(m.id, span, nil)
	start := &audiodv1.Envelope{Body: &audiodv1.Envelope_CaptureStart{CaptureStart: &audiodv1.CaptureStart{
		SessionId: m.id, Trace: tracing.Audiod(tracing.Proto(sctx)), PrerollFromMs: max(t.PrerollFromMs, 0),
	}}}
	if err := a.c.Send(start); err != nil {
		tracing.Release(m.id, "send_failed")
		a.dropMic(m.id)
		return nil, err
	}
	out := make(chan []byte)
	go a.runMic(ctx, m, out)
	return out, nil
}

func (a *audioIO) dropMic(id string) {
	a.mu.Lock()
	delete(a.mics, id)
	a.mu.Unlock()
}

func (a *audioIO) runMic(ctx context.Context, m *micSession, out chan []byte) {
	defer close(out)
	defer a.dropMic(m.id)
	code, packets := "ok", 0
	defer func() { tracing.Release(m.id, code, attribute.Int("cadence.packets", packets)) }()
	stop := func() {
		_ = a.c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_CaptureStop{CaptureStop: &audiodv1.CaptureStop{SessionId: m.id}}})
	}
	wait := a.firstWait
	if a.vendorPending.Load() {
		wait = a.pendingWait
	}
	first := time.NewTimer(wait)
	defer first.Stop()
	gotFirst := false
	for {
		var timeout <-chan time.Time
		if !gotFirst {
			timeout = first.C
		}
		select {
		case <-ctx.Done():
			stop()
			return
		case <-a.c.Done():
			// a dead audiod is a failed capture
			code = "audiod_gone"
			a.markFailed(out)
			return
		case <-timeout:
			code = "mic_timeout"
			a.log.Warn("audiod_mic_timeout", "session_id", m.id)
			a.markFailed(out)
			stop()
			return
		case e := <-m.inbox:
			if e.failed {
				code = "capture_failed"
				a.log.Warn("audiod_capture_failed", "session_id", m.id)
				a.markFailed(out)
				return
			}
			if !gotFirst {
				if sp := tracing.Bound(m.id); sp != nil {
					sp.AddEvent("first_packet")
				}
			}
			gotFirst = true
			packets++
			select {
			case out <- e.opus:
			case <-ctx.Done():
				stop()
				return
			case <-a.c.Done():
				a.markFailed(out)
				return
			}
		}
	}
}

type streamWriter struct {
	a    *audioIO
	id   string
	done chan struct{}
	// closed by Abort to release the waiter End started
	aborted chan struct{}

	mu      sync.Mutex
	frames  int
	ended   bool
	abortOK bool
}

func (a *audioIO) Begin(s *cadencev2.StreamStart) (audio.StreamWriter, error) {
	id := s.GetStreamId()
	if !streamID.MatchString(id) {
		return nil, errBadStreamID
	}
	done := make(chan struct{})
	a.mu.Lock()
	a.streams[id] = done
	a.mu.Unlock()
	// the session's playback span, which the controller put on the frame, so audiod's marks of it land there
	start := &audiodv1.Envelope{Body: &audiodv1.Envelope_StreamStart{StreamStart: &audiodv1.StreamStart{
		StreamId: id, Channels: s.GetChannels(), Trace: tracing.Audiod(s.GetTrace()),
	}}}
	if err := a.c.Send(start); err != nil {
		a.dropStream(id)
		return nil, err
	}
	return &streamWriter{a: a, id: id, done: done, aborted: make(chan struct{})}, nil
}

func (a *audioIO) dropStream(id string) {
	a.mu.Lock()
	delete(a.streams, id)
	a.mu.Unlock()
}

func (w *streamWriter) Write(seq uint32, opus []byte) error {
	if len(opus) > maxOpusBytes {
		return errOpusTooLarge
	}
	w.mu.Lock()
	if w.ended || w.abortOK {
		w.mu.Unlock()
		return errStreamClosed
	}
	w.frames++
	w.mu.Unlock()
	env := &audiodv1.Envelope{Body: &audiodv1.Envelope_AudioDown{AudioDown: &audiodv1.AudioDown{StreamId: w.id, Seq: seq, Opus: opus}}}
	return w.a.c.Send(env)
}

// End asks audiod to finish the stream and returns at once; a goroutine waits
// for StreamDone, at most the stream's duration plus a grace, and only logs.
func (w *streamWriter) End() error {
	w.mu.Lock()
	if w.ended || w.abortOK {
		w.mu.Unlock()
		return nil
	}
	w.ended = true
	frames := w.frames
	w.mu.Unlock()
	env := &audiodv1.Envelope{Body: &audiodv1.Envelope_StreamEnd{StreamEnd: &audiodv1.StreamEnd{StreamId: w.id}}}
	if err := w.a.c.Send(env); err != nil {
		w.a.dropStream(w.id)
		return err
	}
	go w.awaitDone(frames)
	return nil
}

func (w *streamWriter) awaitDone(frames int) {
	t := time.NewTimer(time.Duration(frames)*frameDuration + doneGrace)
	defer t.Stop()
	select {
	case <-w.done:
	case <-w.aborted:
	case <-w.a.c.Done():
		tracing.Release(w.id, "audiod_gone")
		w.a.dropStream(w.id)
	case <-t.C:
		tracing.Release(w.id, "timeout")
		w.a.dropStream(w.id)
		w.a.log.Warn("audiod_stream_timeout", "stream_id", w.id, "frames", frames)
	}
}

// Abort cuts the stream at once, also one End already sent that still plays,
// and never waits.
func (w *streamWriter) Abort() {
	w.mu.Lock()
	if w.abortOK {
		w.mu.Unlock()
		return
	}
	w.abortOK = true
	w.mu.Unlock()
	close(w.aborted)
	w.a.dropStream(w.id)
	_ = w.a.c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_StreamCancel{StreamCancel: &audiodv1.StreamCancel{StreamId: w.id}}})
}

func (a *audioIO) markFailed(out <-chan []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failedMic = out
}

// MicFailure tells a packets channel closed by a failed capture from one that
// ended normally.
func (a *audioIO) MicFailure(packets <-chan []byte) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.failedMic != nil && a.failedMic == packets
}
