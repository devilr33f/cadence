package uniproxy

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// client voice streams are odd, as the stock client numbers them
const (
	firstStreamID = 1
	streamIDStep  = 2
	asrOK         = 0
)

type stt struct{ cfg *config }

// Start opens one socket for one listen. Every utterance on it is a VoiceInput with its own stream; uniproxy's end of
// utterance is the final, and an utterance that ends empty leaves the next frame to open another (v2 has no device
// end of speech for the provider: the coordinator takes the first final with text)
func (p *stt) Start(ctx context.Context, o providers.STTOptions) (providers.STTStream, error) {
	cfg := p.cfg.withLanguage(o.Language)
	sctx, cancel := context.WithCancel(ctx)
	s := &sttStream{cfg: cfg, ctx: sctx, cancel: cancel, events: make(chan providers.STTEvent, 16), nextStream: firstStreamID}
	t, err := dial(ctx, cfg, handlers{message: s.onMessage, end: s.onEnd})
	if err != nil {
		cancel()
		var de dialError
		errors.As(err, &de)
		return nil, &providers.DetailError{Code: providers.CodeSTTUnavailable, Detail: de.cause()}
	}
	s.mu.Lock()
	s.t = t
	s.mu.Unlock()
	return s, nil
}

type utterance struct {
	mux         *oggMuxer
	messageID   string
	streamID    uint32
	timer       *time.Timer
	lastPartial string
	spoke       bool
}

type sttStream struct {
	cfg    *config
	ctx    context.Context
	cancel context.CancelFunc
	events chan providers.STTEvent

	mu         sync.Mutex
	t          *transport
	utt        *utterance
	nextStream uint32
	sequence   uint64
	ended      bool
	closeOnce  sync.Once
	// counts for the uniproxy_stt line: numbers alone (rule 8)
	frames, results, heard, finals int
	code                           string
}

func (s *sttStream) Events() <-chan providers.STTEvent { return s.events }

func (s *sttStream) Write(opus []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || s.t == nil {
		return errors.New(providers.CodeSTTFailed)
	}
	u := s.utt
	if u == nil {
		u = &utterance{
			mux: newOggMuxer(rand.Uint32()), messageID: uuid.NewString(), streamID: s.nextStream, //nolint:gosec // an ogg serial, not a secret
		}
		s.nextStream += streamIDStep
		if s.t.sendEvent(voiceInput(voiceInputParams{
			messageID: u.messageID, streamID: u.streamID, requestID: uuid.NewString(), sequence: s.sequence,
			oauthToken: s.cfg.oauthToken, app: s.cfg.app(),
		})) != nil || s.t.sendMedia(u.streamID, u.mux.headerPages()) != nil {
			return errors.New(providers.CodeSTTFailed)
		}
		s.sequence++
		s.utt = u
	}
	s.frames++
	page, err := u.mux.page(opus)
	if err != nil || s.t.sendMedia(u.streamID, page) != nil {
		return errors.New(providers.CodeSTTFailed)
	}
	return nil
}

func (s *sttStream) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.ended = true
		log := s.cfg.log
		if log == nil {
			log = slog.Default()
		}
		code := s.code
		if code == "" {
			code = "ok"
		}
		log.Info("uniproxy_stt", "frames", s.frames, "utterances", s.sequence, "results", s.results,
			"heard", s.heard, "finals", s.finals, "code", code)
		t, u := s.t, s.utt
		s.release()
		s.mu.Unlock()
		// first, so a read loop blocked handing over an event lets go and the close below does not wait on it
		s.cancel()
		if t != nil {
			if u != nil {
				_ = t.sendEvent(streamClose(u.streamID, u.messageID, closeCancel))
			}
			t.close()
		}
	})
	return nil
}

// release forgets the utterance; the caller holds mu
func (s *sttStream) release() {
	if s.utt != nil && s.utt.timer != nil {
		s.utt.timer.Stop()
	}
	s.utt = nil
}

func (s *sttStream) emit(e providers.STTEvent) {
	select {
	case s.events <- e:
	case <-s.ctx.Done():
	}
}

// fail ends the stream with code and its cause's token; the read loop then closes events
func (s *sttStream) fail(code, detail string) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.code = code
	t, u := s.t, s.utt
	s.release()
	s.mu.Unlock()
	if t != nil && u != nil {
		_ = t.sendEvent(streamClose(u.streamID, u.messageID, closeCancel))
	}
	s.emit(providers.STTEvent{Kind: providers.STTFailed, Code: code, Detail: detail})
	if t != nil {
		go t.close()
	}
}

func (s *sttStream) onEnd(e ending) {
	if o := e.outcome; o != "" {
		code := providers.CodeSTTFailed
		if o == outcomeInvalidAuth || o == outcomeGoAway || o == outcomeClosed {
			code = providers.CodeSTTUnavailable
		}
		s.fail(code, e.detail())
	}
	close(s.events)
}

func (s *sttStream) onMessage(in incoming) {
	if in.control {
		if in.hasClose {
			s.onServerClose(in.streamID, in.closeReason)
		}
		return
	}
	// InputStartAck only confirms the stream; it is not speech
	if in.name == evAsrResult && in.asr != nil {
		s.onAsr(in.header.refMessageID, in.asr)
	}
}

// onAsr is correlated by the VoiceInput's message id alone; the response's stream id is not required
func (s *sttStream) onAsr(ref string, r *asrResult) {
	s.mu.Lock()
	u := s.utt
	if s.ended || u == nil || ref != u.messageID {
		s.mu.Unlock()
		return
	}
	s.results++
	if r.responseCode != asrOK {
		t := s.t
		s.mu.Unlock()
		detail := "asr_error_" + strconv.FormatUint(r.responseCode, 10)
		t.mark(outcomeASRError, detail, attribute.Int64(attrASRCode, int64(r.responseCode))) //nolint:gosec // a small status enum
		s.fail(providers.CodeSTTFailed, detail)
		return
	}
	text := recognizedText(r.recognitions)
	if text != "" {
		s.heard++
	}
	if r.endOfUtt {
		s.finals++
		// the next frame opens another utterance
		s.release()
		s.mu.Unlock()
		if text != "" {
			s.emit(providers.STTEvent{Kind: providers.STTFinal, Text: text})
		}
		return
	}
	if text == "" || text == u.lastPartial {
		s.mu.Unlock()
		return
	}
	u.lastPartial = text
	started := !u.spoke
	if started {
		u.spoke = true
		u.timer = time.AfterFunc(s.cfg.requestTimeout, func() { s.onTimeout(u) })
	}
	s.mu.Unlock()
	if started {
		s.emit(providers.STTEvent{Kind: providers.STTSpeechStarted})
	}
	s.emit(providers.STTEvent{Kind: providers.STTPartial, Text: text})
}

func (s *sttStream) onServerClose(streamID uint32, reason uint64) {
	s.mu.Lock()
	u := s.utt
	// stream 0 means the field was absent: it never matches an odd client stream
	if s.ended || u == nil || streamID != u.streamID {
		s.mu.Unlock()
		return
	}
	if reason != closeOK {
		t := s.t
		s.mu.Unlock()
		detail := "stream_closed_" + strconv.FormatUint(reason, 10)
		t.mark(outcomeStreamClosed, detail, attribute.Int64(attrStreamClose, int64(reason))) //nolint:gosec // a small reason enum
		s.fail(providers.CodeSTTFailed, detail)
		return
	}
	s.release()
	s.mu.Unlock()
}

func (s *sttStream) onTimeout(u *utterance) {
	s.mu.Lock()
	live, t := s.utt == u, s.t
	s.mu.Unlock()
	if live {
		t.mark(outcomeTimeout, string(outcomeTimeout))
		s.fail(providers.CodeSTTFailed, string(outcomeTimeout))
	}
}

// recognizedText is the first nonempty normalized text in server order, else the first alternative's words
func recognizedText(rs []recognition) string {
	for _, r := range rs {
		if n := strings.TrimSpace(r.normalized); n != "" {
			return n
		}
	}
	if len(rs) == 0 {
		return ""
	}
	var words []string
	for _, w := range rs[0].words {
		if w = strings.TrimSpace(w); w != "" {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}
