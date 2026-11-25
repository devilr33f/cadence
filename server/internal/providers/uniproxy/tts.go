package uniproxy

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const defaultTTSRate = 24000

var errTTS = errors.New(providers.CodeTTSFailed)

type tts struct{ cfg *config }

// Open dials one socket for one reply, so the handshake overlaps the agent's text. Each speakable chunk is one item,
// and the next is requested only once the previous stream closed OK: requested, speaking (TtsSpeak), closed
func (p *tts) Open(ctx context.Context, o providers.TTSOptions) (providers.TTSStream, error) {
	cfg := p.cfg.withLanguage(o.Language)
	if o.Voice != "" {
		cfg.voice = o.Voice
	}
	rate := o.SampleRate
	if rate <= 0 {
		rate = defaultTTSRate
	}
	// fails here, before any socket, in a build without opus
	if _, err := audio.NewDecoder(rate, 1); err != nil {
		return nil, errTTS
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &ttsStream{cfg: cfg, rate: rate, ctx: sctx, cancel: cancel, out: make(chan []int16, 8)}
	t, err := dial(ctx, cfg, handlers{message: s.onMessage, media: s.onMedia, end: s.onEnd})
	if err != nil {
		cancel()
		var de dialError
		errors.As(err, &de)
		return nil, &providers.DetailError{Code: providers.CodeTTSFailed, Detail: de.cause()}
	}
	s.mu.Lock()
	s.t = t
	s.mu.Unlock()
	return s, nil
}

// item is one TtsGenerate and the server stream that answers it
type item struct {
	messageID string
	timer     *time.Timer
	// from the TtsSpeak; 0 until then
	streamID uint32
	audio    *speechDecoder
}

type ttsStream struct {
	cfg    *config
	rate   int
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	t         *transport
	pending   []string
	item      *item
	flushed   bool
	done      bool
	err       error
	closeOnce sync.Once

	outMu     sync.Mutex
	out       chan []int16
	outClosed bool
}

func (s *ttsStream) Audio() <-chan []int16 { return s.out }

func (s *ttsStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// speakable: a chunk with no letter or digit (an emoji, a dash) has nothing to say, and how yandex answers one is
// unknown: a failure would end the whole reply
func speakable(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

func (s *ttsStream) Speak(text string) error {
	text = strings.TrimSpace(text)
	s.mu.Lock()
	if s.done {
		failed := s.err != nil
		s.mu.Unlock()
		if failed {
			return errTTS
		}
		return nil
	}
	if speakable(text) {
		s.pending = append(s.pending, text)
	}
	s.mu.Unlock()
	s.advance()
	return nil
}

func (s *ttsStream) Flush() error {
	s.mu.Lock()
	s.flushed = true
	s.mu.Unlock()
	s.advance()
	return nil
}

// Close ends the stream without an error; a stream the server announced and has not closed is cancelled
func (s *ttsStream) Close() error {
	s.shutdown(nil)
	return nil
}

// advance requests the next item once none is in flight, or finishes once the text is flushed and all of it spoken
func (s *ttsStream) advance() {
	s.mu.Lock()
	if s.done || s.item != nil || s.t == nil {
		s.mu.Unlock()
		return
	}
	if len(s.pending) == 0 {
		finished := s.flushed
		s.mu.Unlock()
		if finished {
			s.shutdown(nil)
		}
		return
	}
	text := s.pending[0]
	s.pending = s.pending[1:]
	it := &item{messageID: uuid.NewString()}
	it.timer = time.AfterFunc(s.cfg.requestTimeout, func() { s.onTimeout(it) })
	s.item = it
	t := s.t
	s.mu.Unlock()
	// no client stream id: the server announces its own in TtsSpeak
	if t.sendEvent(ttsGenerate(it.messageID, text, s.cfg.lang, s.cfg.voice)) != nil {
		s.shutdown(errTTS)
	}
}

func (s *ttsStream) onMessage(in incoming) {
	s.mu.Lock()
	it := s.item
	if s.done || it == nil {
		s.mu.Unlock()
		return
	}
	switch {
	case in.control:
		if !in.hasClose || it.streamID == 0 || in.streamID != it.streamID {
			s.mu.Unlock()
			return
		}
		// the server ended the stream itself: nothing is left to cancel
		it.timer.Stop()
		s.item = nil
		t := s.t
		s.mu.Unlock()
		if in.closeReason != closeOK {
			s.failWith(t, outcomeStreamClosed, "stream_closed_"+strconv.FormatUint(in.closeReason, 10),
				attribute.Int64(attrStreamClose, int64(in.closeReason))) //nolint:gosec // a small reason enum
			return
		}
		s.advance()
	case in.name == evTtsSpeak && in.tts != nil:
		sp := in.tts
		if it.streamID != 0 {
			s.mu.Unlock()
			// a repeat changes nothing; another stream for the same text would be a substream
			if sp.streamID != it.streamID {
				s.failWith(s.transport(), outcomeBadStream, "substream")
			}
			return
		}
		if sp.streamID == 0 {
			t := s.t
			s.mu.Unlock()
			s.failWith(t, outcomeBadStream, "no_stream_id")
			return
		}
		// recorded first, so a stream refused below is still cancelled
		it.streamID = sp.streamID
		// lazy streaming waits for chunk requests cadence never sends; any other format is not ogg opus
		if sp.lazy || sp.format != opusFormat {
			t := s.t
			s.mu.Unlock()
			s.failWith(t, outcomeBadStream, "unsupported_stream")
			return
		}
		it.audio = newSpeechDecoder(s.rate)
		s.mu.Unlock()
	default:
		s.mu.Unlock()
	}
}

// onMedia runs on the read loop alone, so the item's decoder is never shared
func (s *ttsStream) onMedia(streamID uint32, b []byte) {
	s.mu.Lock()
	it := s.item
	if s.done || it == nil || it.audio == nil || streamID != it.streamID {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	pcm, err := it.audio.push(b)
	if err != nil {
		s.failWith(s.transport(), outcomeDecode, string(outcomeDecode))
		return
	}
	if len(pcm) > 0 {
		s.deliver(pcm)
	}
}

func (s *ttsStream) onEnd(e ending) {
	if e.outcome != "" {
		s.shutdown(&providers.DetailError{Code: providers.CodeTTSFailed, Detail: e.detail()})
	}
}

func (s *ttsStream) onTimeout(it *item) {
	s.mu.Lock()
	live, t := s.item == it, s.t
	s.mu.Unlock()
	if live {
		s.failWith(t, outcomeTimeout, string(outcomeTimeout))
	}
}

func (s *ttsStream) transport() *transport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.t
}

// failWith marks the stream's own failure on the socket's span, then ends the stream with its detail
func (s *ttsStream) failWith(t *transport, o outcome, detail string, kv ...attribute.KeyValue) {
	t.mark(o, detail, kv...)
	s.shutdown(&providers.DetailError{Code: providers.CodeTTSFailed, Detail: detail})
}

// deliver hands pcm on unless the stream is closed or its context ended; it blocks while the reply's encoder catches up
func (s *ttsStream) deliver(pcm []int16) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.outClosed {
		return
	}
	select {
	case s.out <- pcm:
	case <-s.ctx.Done():
	}
}

// shutdown ends the stream once: err nil after a clean flush or Close, else the fixed code. Audio closes after the
// last pcm handed on; what is already queued in it is still read
func (s *ttsStream) shutdown(err error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.done = true
		s.err = err
		t, it := s.t, s.item
		if it != nil {
			it.timer.Stop()
		}
		s.item = nil
		s.mu.Unlock()
		if t != nil {
			if it != nil && it.streamID != 0 {
				_ = t.sendEvent(streamClose(it.streamID, it.messageID, closeCancel))
			}
			go t.close()
		}
		// unblocks a handover in flight (a clean finish has none: it runs after the last), so out can close
		s.cancel()
		s.outMu.Lock()
		s.outClosed = true
		close(s.out)
		s.outMu.Unlock()
	})
}

// speechDecoder is one synthesis stream's downlink: ogg bytes in arrival order, opus packets, mono pcm at rate.
// uniproxy's synthesis is mono opus; every logical stream opens with an id header and gets a fresh decoder
type speechDecoder struct {
	rate    int
	demux   *oggDemuxer
	dec     audio.Decoder
	decoded bool
	// the end of the previous push, so an id header split across frames is still found
	tail []byte
}

func newSpeechDecoder(rate int) *speechDecoder {
	return &speechDecoder{rate: rate, demux: newOggDemuxer()}
}

func (d *speechDecoder) push(media []byte) ([]int16, error) {
	var out []int16
	from := 0
	for _, at := range d.streamStarts(media) {
		// the bytes before a new id header can only complete pages of the stream before it
		if err := d.decode(media[from:at], &out); err != nil {
			return nil, err
		}
		if d.decoded {
			d.dec, d.decoded = nil, false
		}
		from = at
	}
	if err := d.decode(media[from:], &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *speechDecoder) decode(b []byte, out *[]int16) error {
	for _, p := range d.demux.push(b) {
		// opus would decode an empty packet as loss concealment
		if len(p) == 0 {
			continue
		}
		if d.dec == nil {
			dec, err := audio.NewDecoder(d.rate, 1)
			if err != nil {
				return err
			}
			d.dec = dec
		}
		pcm, err := d.dec.Decode(p)
		if err != nil {
			return err
		}
		d.decoded = true
		*out = append(*out, pcm...)
	}
	return nil
}

// streamStarts are the offsets in media where an id header begins; one that began in the previous push gives 0
func (d *speechDecoder) streamStarts(media []byte) []int {
	data := append(append([]byte(nil), d.tail...), media...)
	var starts []int
	for at := 0; ; {
		i := bytes.Index(data[at:], opusHead)
		if i < 0 {
			break
		}
		starts = append(starts, max(at+i-len(d.tail), 0))
		at += i + len(opusHead)
	}
	d.tail = append([]byte(nil), data[max(len(data)-(len(opusHead)-1), 0):]...)
	return starts
}
