package voice

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/voice/cmdguard"
)

// stream is a tts stream being opened; tts and err are set once opened closes.
// A nil tts after it closed is a failed open.
type stream struct {
	opened chan struct{}
	tts    providers.TTSStream
	err    error
}

// segment is one tts stream of the reply.
type segment struct {
	gen     uint64
	st      *stream
	stop    context.CancelFunc // ends its encoder
	encoded chan struct{}      // closed once its encoder returned
	spoke   bool
}

// speaker turns the agent's speech into packets: sentences go to tts as they
// complete, through the command-word guard, which joins them (M6a-D6). A
// discard (M3-R9) drops the unfinished sentence, a word the guard holds, and
// everything not yet sent to the device: the segment's stream is abandoned,
// its packets stop being live, and what is said next goes to a new stream,
// opened at once.
type speaker struct {
	s       *session
	ctx     context.Context // the reply's
	p       plan
	packets chan packet
	gen     atomic.Uint64 // the live segment's
	failed  atomic.Bool
	onFail  func()

	encoders sync.WaitGroup
	// owned by the speak goroutine once it runs
	text  chunker
	guard *cmdguard.Stream
	seg   *segment
	// what tts was given, after the guard: the next follow-up's echo check
	spoken strings.Builder
}

// newSpeaker starts opening the first segment's stream. The packets hold what
// the turn's time can play, so a live encoder never waits on the pacer.
func newSpeaker(ctx context.Context, s *session, p plan, onFail func()) *speaker {
	dl, _ := ctx.Deadline()
	room := max(int(time.Until(dl)/frameDuration), 1)
	k := &speaker{s: s, ctx: ctx, p: p, packets: make(chan packet, room), onFail: onFail, guard: cmdguard.NewStream(p.language)}
	k.begin()
	return k
}

// run speaks what the agent says until its speech ends: at its End, which
// comes before the agent commits the turn (M3-R11), else when outs closes.
// Whatever follows the End is drained unsaid.
func (k *speaker) run(outs <-chan agent.Out) {
	ended := false
	for {
		select {
		case <-k.ctx.Done():
			return
		case o, ok := <-outs:
			switch {
			case !ok:
				if !ended {
					k.finish()
				}
				return
			case ended:
				// drained unsaid
			case o.End:
				k.finish()
				ended = true
			case o.Discard:
				k.discard()
			default:
				k.speak(k.text.push(o.Speech))
			}
		}
	}
}

// finish says what the chunker and the guard still hold and ends the live segment.
func (k *speaker) finish() {
	k.speak(k.text.rest())
	k.say(k.guard.Flush())
	k.end()
}

// speak gives tts what the guard lets go of the chunks.
func (k *speaker) speak(chunks []cmdguard.Chunk) {
	for _, c := range chunks {
		k.say(k.guard.Push(c))
	}
}

// done runs after run, a panic included: once every encoder returned, the
// live stream is closed and the packets end.
func (k *speaker) done() {
	k.encoders.Wait()
	k.closeStream(k.seg)
	close(k.packets)
}

// open dials a tts stream on its own goroutine, under the reply's ctx and
// the segment's span.
func (k *speaker) open(span trace.Span) *stream {
	st := &stream{opened: make(chan struct{})}
	ctx := trace.ContextWithSpan(k.ctx, span)
	k.s.goSafe("tts_open", func() {
		st.tts, st.err = k.p.tts.Open(ctx, k.p.ttsOptions())
	}, func() { close(st.opened) })
	return st
}

// ready waits for st to open; false when it failed or the reply ended first.
func (k *speaker) ready(ctx context.Context, st *stream) (providers.TTSStream, bool) {
	select {
	case <-st.opened:
		return st.tts, st.err == nil && st.tts != nil
	case <-ctx.Done():
		return nil, false
	}
}

// begin makes a new stream the live segment. Its encoder starts once the
// stream opened and the previous encoder returned, since they share the opus
// encoder; a stream that failed to open fails the turn only when there is
// speech for it (say), never here.
func (k *speaker) begin() {
	ctx, stop := context.WithCancel(k.ctx)
	var prev chan struct{}
	if k.seg != nil {
		prev = k.seg.encoded
	}
	gen := k.gen.Load()
	ctx, tt := startTTSSpan(ctx, k.s.values.TTS, gen)
	seg := &segment{gen: gen, st: k.open(tt.span), stop: stop, encoded: make(chan struct{})}
	k.seg = seg
	k.encoders.Add(1)
	// the segment's span ends with its encoder: a failed open or encode is its code
	var failed error
	k.s.goSafe("encode", func() {
		if prev != nil {
			<-prev
		}
		tts, ok := k.ready(ctx, seg.st)
		if !ok {
			// ctx alive: ready saw the stream open, and fail
			if ctx.Err() == nil {
				failed = openErr(seg.st)
			}
			return
		}
		if err := encode(ctx, tts, k.p.enc, k.p.channels, seg.gen, k.packets, tt); err != nil && ctx.Err() == nil {
			failed = err
			k.fail()
		}
	}, k.encoders.Done, func() { close(seg.encoded) }, stop, func() {
		tt.end(failed)
		if failed != nil {
			k.s.noteCause(orDetail(failed, ""))
		}
	})
}

// say sends guarded text to the live segment once its stream opened. Tts
// joins what it is sent until the flush; the guard put back the space the
// chunker trimmed.
func (k *speaker) say(texts []string) {
	for _, text := range texts {
		if k.failed.Load() {
			return
		}
		tts, ok := k.ready(k.ctx, k.seg.st)
		if !ok {
			// the reply's own end is not the tts's failure
			if k.ctx.Err() == nil {
				k.fail()
			}
			return
		}
		if tts.Speak(text) != nil {
			k.fail()
			return
		}
		k.spoken.WriteString(text)
		k.seg.spoke = true
	}
}

// discard drops the unfinished sentence and, once tts was given speech, the
// segment with whatever it still owed; the next stream starts opening now,
// so what is said next does not wait on a handshake.
func (k *speaker) discard() {
	k.text.reset()
	k.guard.Reset()
	if !k.seg.spoke {
		return
	}
	// the next segment starts without the joining space
	k.spoken.WriteString(" ")
	k.gen.Add(1)
	k.seg.stop()
	k.closeStream(k.seg)
	k.begin()
}

// end flushes the live segment. One that said nothing or failed is stopped
// and closed instead: an empty flush may never be answered, and the audio
// would wait for the turn deadline, or for a stream still opening.
func (k *speaker) end() {
	if k.seg.spoke && !k.failed.Load() {
		_ = k.seg.st.tts.Flush()
		return
	}
	k.seg.stop()
	k.closeStream(k.seg)
}

// closeStream closes seg's stream once it opened; aura's close waits for the
// upstream close handshake.
func (k *speaker) closeStream(seg *segment) {
	st := seg.st
	k.s.goSafe("tts_close", func() {
		<-st.opened
		if st.tts != nil {
			_ = st.tts.Close()
		}
	})
}

// fail records a tts failure and ends the agent's turn.
func (k *speaker) fail() {
	if k.failed.CompareAndSwap(false, true) {
		k.onFail()
	}
}

// live reports a packet of the live segment.
func (k *speaker) live(p packet) bool { return p.gen == k.gen.Load() }
