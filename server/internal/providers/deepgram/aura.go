package deepgram

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	defaultVoice   = "aura-2-thalia-en"
	defaultTTSRate = 24000
	maxSpeakRunes  = 2000
)

type AuraConfig struct {
	BaseURL string
	// Log receives provider_panic; nil uses slog default
	Log *slog.Logger
}

type aura struct {
	log    *slog.Logger
	base   string
	key    string
	client *http.Client
}

func NewAura(c AuraConfig, apiKey string, client *http.Client) providers.TTS {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	return &aura{log: c.Log, base: c.BaseURL, key: apiKey, client: client}
}

func (a *aura) Open(ctx context.Context, o providers.TTSOptions) (providers.TTSStream, error) {
	voice, rate := o.Voice, o.SampleRate
	if voice == "" {
		voice = defaultVoice
	}
	if rate <= 0 {
		rate = defaultTTSRate
	}
	q := url.Values{
		"model":       {voice},
		"encoding":    {"linear16"},
		"sample_rate": {strconv.Itoa(rate)},
	}
	conn, err := dial(ctx, a.base, "/v1/speak", q, a.key, a.client, providers.CodeTTSFailed)
	if err != nil {
		logFailed(a.log, "tts", "deepgram-aura", providers.Detail(err))
		return nil, err
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &auraStream{conn: conn, ctx: sctx, cancel: cancel, out: make(chan []int16), log: a.log}
	go s.read(a.log)
	return s, nil
}

type auraStream struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	out    chan []int16
	once   sync.Once
	// written only by read, before out closes
	err error
	log *slog.Logger
}

func (s *auraStream) Audio() <-chan []int16 { return s.out }

func (s *auraStream) Err() error { return s.err }

// fail records the fixed code with detail, the cause's token, unless the caller is already tearing the stream down
func (s *auraStream) fail(detail string) {
	if s.ctx.Err() == nil {
		logFailed(s.log, "tts", "deepgram-aura", detail)
		s.err = &providers.DetailError{Code: providers.CodeTTSFailed, Detail: detail}
	}
}

func (s *auraStream) send(v any) error {
	b, err := json.Marshal(v)
	if err == nil {
		err = s.conn.Write(s.ctx, websocket.MessageText, b)
	}
	if err != nil {
		return errors.New(providers.CodeTTSFailed)
	}
	return nil
}

type speakMsg struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func (s *auraStream) Speak(text string) error {
	for _, part := range splitRunes(text, maxSpeakRunes) {
		if err := s.send(speakMsg{Type: "Speak", Text: part}); err != nil {
			return err
		}
	}
	return nil
}

func (s *auraStream) Flush() error { return s.send(speakMsg{Type: "Flush"}) }

func (s *auraStream) Close() error {
	s.once.Do(func() {
		s.cancel()
		_ = s.conn.Close(websocket.StatusNormalClosure, "")
	})
	return nil
}

func splitRunes(text string, n int) []string {
	r := []rune(text)
	var out []string
	for len(r) > n {
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return append(out, string(r))
}

// read ends the audio channel on Flushed, on failure and on close alike
func (s *auraStream) read(log *slog.Logger) {
	defer s.shutdown()
	if providers.Guard(log, "deepgram-aura", providers.CodeTTSFailed, s.readLoop) {
		s.fail("protocol")
	}
}

func (s *auraStream) readLoop() {
	var carry []byte
	for {
		typ, b, err := s.conn.Read(s.ctx)
		if err != nil {
			s.fail(readDetail(err))
			return
		}
		if decodeHook != nil {
			decodeHook(b)
		}
		if typ == websocket.MessageBinary {
			carry = append(carry, b...)
			n := len(carry) / 2
			if n == 0 {
				continue
			}
			pcm := make([]int16, n)
			for i := range pcm {
				pcm[i] = int16(binary.LittleEndian.Uint16(carry[2*i:])) //nolint:gosec // two's complement reinterpretation is intended
			}
			carry = append([]byte(nil), carry[2*n:]...)
			select {
			case s.out <- pcm:
			case <-s.ctx.Done():
				return
			}
			continue
		}
		var m speakMsg
		if json.Unmarshal(b, &m) != nil {
			s.fail("protocol")
			return
		}
		switch m.Type {
		case "Flushed":
			return
		case "Error":
			s.fail("error_message")
			return
		}
	}
}

func (s *auraStream) shutdown() {
	close(s.out)
	_ = s.send(speakMsg{Type: "Close"})
	s.once.Do(func() {
		s.cancel()
		_ = s.conn.Close(websocket.StatusNormalClosure, "")
	})
}
