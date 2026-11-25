// Package deepgram adapts Deepgram Flux (STT) and Aura (TTS) websockets to the
// provider contracts. Upstream text never reaches an error, event or log.
package deepgram

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	defaultBaseURL  = "wss://api.deepgram.com"
	defaultModel    = "flux-general-en"
	defaultEOT      = 0.8
	defaultRate     = 16000
	readLimitBytes  = 256 << 10
	closeSendWindow = time.Second
)

type FluxConfig struct {
	BaseURL      string
	Model        string
	EOTThreshold float64
	// Log receives provider_panic; nil uses slog default
	Log *slog.Logger
}

type flux struct {
	cfg    FluxConfig
	key    string
	client *http.Client
}

func NewFlux(c FluxConfig, apiKey string, client *http.Client) providers.STT {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.Model == "" {
		c.Model = defaultModel
	}
	if c.EOTThreshold == 0 {
		c.EOTThreshold = defaultEOT
	}
	return &flux{cfg: c, key: apiKey, client: client}
}

// dial opens an authenticated socket; the error is fixed text, never upstream's.
func dial(ctx context.Context, base, path string, q url.Values, key string, client *http.Client, code string) (*websocket.Conn, error) {
	u := strings.TrimRight(base, "/") + path + "?" + q.Encode()
	c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: http.Header{"Authorization": {"Token " + key}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		status := 0
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			status = resp.StatusCode
		}
		return nil, &providers.DetailError{Code: code, Detail: dialDetail(status, err)}
	}
	c.SetReadLimit(readLimitBytes)
	return c, nil
}

func (f *flux) Start(ctx context.Context, o providers.STTOptions) (providers.STTStream, error) {
	rate := o.SampleRate
	if rate <= 0 {
		rate = defaultRate
	}
	q := url.Values{
		"model":         {f.cfg.Model},
		"encoding":      {"opus"},
		"sample_rate":   {strconv.Itoa(rate)},
		"eot_threshold": {strconv.FormatFloat(f.cfg.EOTThreshold, 'f', -1, 64)},
	}
	conn, err := dial(ctx, f.cfg.BaseURL, "/v2/listen", q, f.key, f.client, providers.CodeSTTUnavailable)
	if err != nil {
		logFailed(f.cfg.Log, "stt", "deepgram-flux", providers.Detail(err))
		return nil, err
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &fluxStream{conn: conn, ctx: sctx, cancel: cancel, events: make(chan providers.STTEvent, 16), log: f.cfg.Log}
	go s.read(f.cfg.Log)
	return s, nil
}

type fluxStream struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	events chan providers.STTEvent

	closeOnce sync.Once
	mu        sync.Mutex
	closing   bool
	log       *slog.Logger
}

func (s *fluxStream) Events() <-chan providers.STTEvent { return s.events }

func (s *fluxStream) Write(opus []byte) error {
	if err := s.conn.Write(s.ctx, websocket.MessageBinary, opus); err != nil {
		return errors.New(providers.CodeSTTFailed)
	}
	return nil
}

func (s *fluxStream) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		wc, cancel := context.WithTimeout(context.Background(), closeSendWindow)
		_ = s.conn.Write(wc, websocket.MessageText, []byte(`{"type":"CloseStream"}`))
		cancel()
		_ = s.conn.Close(websocket.StatusNormalClosure, "")
		s.cancel()
	})
	return nil
}

func (s *fluxStream) emit(e providers.STTEvent) bool {
	select {
	case s.events <- e:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// fail ends the stream with detail, the cause's token, unless the caller is closing it
func (s *fluxStream) fail(detail string) {
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if !closing {
		logFailed(s.log, "stt", "deepgram-flux", detail)
		s.emit(providers.STTEvent{Kind: providers.STTFailed, Code: providers.CodeSTTFailed, Detail: detail})
	}
	_ = s.conn.CloseNow()
}

type fluxMessage struct {
	Type       string `json:"type"`
	Event      string `json:"event"`
	TurnIndex  int    `json:"turn_index"`
	Transcript string `json:"transcript"`
}

func (s *fluxStream) read(log *slog.Logger) {
	defer close(s.events)
	if providers.Guard(log, "deepgram-flux", providers.CodeSTTFailed, s.readLoop) {
		s.fail("protocol")
	}
}

func (s *fluxStream) readLoop() {
	lastEnded := -1
	for {
		typ, b, err := s.conn.Read(s.ctx)
		if err != nil {
			s.fail(readDetail(err))
			return
		}
		if decodeHook != nil {
			decodeHook(b)
		}
		var m fluxMessage
		if typ != websocket.MessageText || json.Unmarshal(b, &m) != nil {
			s.fail("protocol")
			return
		}
		switch m.Type {
		case "Error":
			// its description is upstream text: the type alone
			s.fail("error_message")
			return
		case "TurnInfo":
			if m.TurnIndex <= lastEnded {
				continue
			}
			switch m.Event {
			case "StartOfTurn":
				if !s.emit(providers.STTEvent{Kind: providers.STTSpeechStarted}) {
					return
				}
			case "Update", "EagerEndOfTurn", "TurnResumed":
				// flux sends the turn's transcript so far about every 240 ms
				if text := strings.TrimSpace(m.Transcript); text != "" {
					if !s.emit(providers.STTEvent{Kind: providers.STTPartial, Text: text}) {
						return
					}
				}
			case "EndOfTurn":
				lastEnded = m.TurnIndex
				if text := strings.TrimSpace(m.Transcript); text != "" {
					if !s.emit(providers.STTEvent{Kind: providers.STTFinal, Text: text}) {
						return
					}
				}
			}
		}
	}
}
