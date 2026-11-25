// Package assistant runs turns the owner types in the dashboard: the device's
// own agent turn, woken as a press would be, whose steps the live feed shows
// and whose reply may also play on the device.
package assistant

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// turnLimit bounds a typed turn as the voice coordinator bounds a spoken one
const turnLimit = 60 * time.Second

// ErrBusy is a device that already runs a typed turn.
var ErrBusy = errors.New("assistant: a typed turn is running")

// Request is one typed turn: its text, trimmed, and whether its reply also
// plays on the device.
type Request struct {
	DeviceID, Text, Language string
	Speak                    bool
}

// Deps are the service's collaborators.
type Deps struct {
	// Run runs a typed turn (agent.Runner.RunTyped)
	Run func(ctx context.Context, t agent.Turn) (agent.Result, string)
	// Announce plays text on the device over its music (voice.Coordinator's
	// announcement) and answers its code, "" once it plays
	Announce func(ctx context.Context, deviceID, text, language string) string
	Bus      events.Publisher
	Log      *slog.Logger
}

// Service runs typed turns, one at a time a device, on the server's lifetime,
// not the request's.
type Service struct {
	d    Deps
	base context.Context

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

// New makes the service; base is the server's lifetime, which ends every
// turn still running.
func New(base context.Context, d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{d: d, base: base, running: map[string]bool{}}
}

// Start begins r's turn and returns its id at once: the turn's steps reach
// the live feed under that id, and its end as turn_end (and, when it speaks,
// assistant_speech). The turn outlives ctx, the request's: it is a trace of
// its own, linked to the request's span.
func (s *Service) Start(ctx context.Context, r Request) (string, error) {
	s.mu.Lock()
	if s.running[r.DeviceID] {
		s.mu.Unlock()
		return "", ErrBusy
	}
	s.running[r.DeviceID] = true
	s.mu.Unlock()
	id := "dash_" + rand.Text()
	// the turn outlives the request: its own trace, under the service's life
	_, span := tracing.Background(ctx, "assistant", "assistant.turn",
		attribute.String(tracing.AttrDeviceID, r.DeviceID), attribute.String(tracing.AttrTurnID, id))
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.running, r.DeviceID)
			s.mu.Unlock()
		}()
		s.run(trace.ContextWithSpan(s.base, span), id, r)
		span.End()
	}()
	return id, nil
}

// Wait waits for the turns still running, for tests and shutdown.
func (s *Service) Wait() { s.wg.Wait() }

// Ask runs r's turn to its end under ctx, the caller's, and answers its
// result and all it said, holding the device's one typed turn as Start does:
// ErrBusy while one runs. Speak plays the reply on the device as Start's
// would; the turn's steps reach the live feed under the id as any typed
// turn's.
func (s *Service) Ask(ctx context.Context, r Request) (agent.Result, string, error) {
	s.mu.Lock()
	if s.running[r.DeviceID] {
		s.mu.Unlock()
		return agent.Result{}, "", ErrBusy
	}
	s.running[r.DeviceID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, r.DeviceID)
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, turnLimit)
	defer cancel()
	id := "ask_" + rand.Text()
	_, span := tracing.Background(ctx, "assistant", "assistant.ask",
		attribute.String(tracing.AttrDeviceID, r.DeviceID), attribute.String(tracing.AttrTurnID, id))
	defer span.End()
	res, spoken := s.d.Run(ctx, agent.Turn{
		ID: id, DeviceID: r.DeviceID, Trigger: cadencev2.Trigger_TRIGGER_BUTTON, Text: r.Text, Language: r.Language,
	})
	s.d.Log.Info("assistant_turn", "device_id", r.DeviceID, "turn_id", id, "code", orOK(res.Code), "speak", r.Speak)
	s.speakIfAsked(ctx, id, r, res, spoken)
	return res, spoken, nil
}

// run runs under base, the service's life with the turn's span.
func (s *Service) run(base context.Context, id string, r Request) {
	ctx, cancel := context.WithTimeout(base, turnLimit)
	defer cancel()
	res, spoken := s.d.Run(ctx, agent.Turn{
		ID: id, DeviceID: r.DeviceID, Trigger: cadencev2.Trigger_TRIGGER_BUTTON, Text: r.Text, Language: r.Language,
	})
	s.d.Log.Info("assistant_turn", "device_id", r.DeviceID, "turn_id", id, "code", orOK(res.Code), "speak", r.Speak)
	s.speakIfAsked(ctx, id, r, res, spoken)
}

// speakIfAsked plays a successful turn's reply on its device and publishes
// the speech's code.
func (s *Service) speakIfAsked(ctx context.Context, id string, r Request, res agent.Result, spoken string) {
	if !r.Speak || res.Code != "" || spoken == "" || s.d.Announce == nil {
		return
	}
	code := orOK(s.d.Announce(ctx, r.DeviceID, spoken, r.Language))
	s.d.Log.Info("assistant_speech", "device_id", r.DeviceID, "turn_id", id, "code", code)
	if s.d.Bus != nil {
		s.d.Bus.Publish(events.Event{
			Type: events.AssistantSpeech, DeviceID: r.DeviceID, TurnID: id, At: time.Now(), Data: map[string]any{"code": code},
		})
	}
}

func orOK(code string) string {
	if code == "" {
		return "ok"
	}
	return code
}
