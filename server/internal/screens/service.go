package screens

import (
	"context"
	"log/slog"
	"sync"
	"time"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

const (
	// DuckLevel is the assistant's duck (design §16): a fifth of the level
	DuckLevel  = 0.2
	duckRampMs = 300
	// a MicOpen that no SessionStart answered is forgotten after this
	pendingTTL = 15 * time.Second
	// a turn nothing closed is dropped after this (the voice turn's own bound
	// is well below)
	turnTTL   = 5 * time.Minute
	inputSize = 1024
	// lookupTimeout bounds a turn start's room lookup
	lookupTimeout = 2 * time.Second
	maxSteps      = 12
	maxTurnCards  = 6
)

// Deps are the service's collaborators.
type Deps struct {
	Directory Directory
	// State reads a device's display.screen state
	State  StateFunc
	Online func(deviceID string) bool
	Send   func(deviceID string, env *cadencev2.Envelope) error
	// Playing says whether a device's media session plays now
	Playing func(deviceID string) bool
	// Lang is a screen's language
	Lang func(ctx context.Context, deviceID string) string
	// Name is a device's name as the owner set it, for a MicSource's speaker; nil names none
	Name     func(ctx context.Context, deviceID string) string
	Messages cards.Messages
	// Cards issues the cards a turn shows on each screen
	Cards interface {
		Show(ctx context.Context, deviceID string, s cards.Subject) (*cardsv1.Card, error)
	}
	// Calls, when set, answers each tool call's step line and cards in every state, fresh subjects per call
	// (cards.ToolCards); without it a step is the tool's label and shows no card
	Calls CallCards
	// Live, when set, hears a Home Assistant write's entity card on each of the turn's screens: the screen may follow
	// it under a lease (cards.Leases.Allow)
	Live func(deviceID string, subj cards.Subject)
	// Publish, when set, puts each call card's latest state on the live feed for the dashboard (tool_card): shown,
	// never logged
	Publish  func(e events.Event)
	Contexts *Contexts
	// StopSession ends a device's running voice session as the server
	// (assistant.stop): the voice coordinator's Stop. nil: none can be stopped
	StopSession func(deviceID, sessionID string) bool
	// ResetConversation moves a device's context boundary and notifies its room's
	// screens (the conversation.Resetter's callback). nil: not supported
	ResetConversation func(ctx context.Context, deviceID string) error
	Now               func() time.Time
	Log               *slog.Logger
}

// Service is the room fan-out. Its inputs (the link's frames, the agent's
// bus events, the tools' attachments) are taken in order on one goroutine.
type Service struct {
	d  Deps
	in chan input

	pmu     sync.Mutex
	pending map[string]pendingOpen

	// owned by the run goroutine
	turns map[string]*turn

	// the screen whose assistant button opened each running session, for the target resolver
	bmu     sync.Mutex
	pressed map[string]string
}

type pendingOpen struct {
	screen, speaker string
	at              time.Time
	// the screen asked to stop before the speaker's session started: it is
	// stopped as it starts
	stopped bool
}

type inputKind int

const (
	inFromDevice inputKind = iota + 1
	inToDevice
	inBus
	inAttach
	inClosed
	inLinked
	inStop
	inHeard
	inReset
	inResetTarget
)

type input struct {
	kind inputKind
	dev  string
	env  *cadencev2.Envelope
	ev   events.Event
	// an attachment's session and subject
	session string
	subj    cards.Subject
	// key is an attachment's call (CallKey)
	key string
	// a stop's answer
	stopped chan stopTarget
	// a hypothesis of the session's utterance
	text  string
	final bool
	// a reset target lookup's answer
	target chan string
}

// New builds the service; Run starts it.
func New(d Deps) *Service {
	if d.Messages == nil {
		d.Messages = cards.Builtin
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Contexts == nil {
		d.Contexts = NewContexts(d.Now)
	}
	if d.Playing == nil {
		d.Playing = func(string) bool { return false }
	}
	if d.Online == nil {
		d.Online = func(string) bool { return true }
	}
	return &Service{d: d, in: make(chan input, inputSize), pending: map[string]pendingOpen{}, turns: map[string]*turn{},
		pressed: map[string]string{}}
}

// Contexts is the screen-context store.
func (s *Service) Contexts() *Contexts { return s.d.Contexts }

func (s *Service) push(in input) {
	select {
	case s.in <- in:
	default:
		s.d.Log.Warn("screens_input_dropped", "device_id", in.dev)
	}
}

// Inbound takes a device's frame first: a ScreenContext is the store's
// (true); a session's start and cancel are watched and passed on (false).
func (s *Service) Inbound(deviceID string, env *cadencev2.Envelope) bool {
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_ScreenContext:
		s.d.Contexts.Put(deviceID, b.ScreenContext)
		return true
	case *cadencev2.Envelope_SessionStart, *cadencev2.Envelope_SessionCancel:
		s.push(input{kind: inFromDevice, dev: deviceID, env: env})
	}
	return false
}

// Outbound watches what the voice service sends a device: a session's
// listening, thinking, reply stream and close. It must stay quick.
func (s *Service) Outbound(deviceID string, env *cadencev2.Envelope) {
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_SessionEvent, *cadencev2.Envelope_SessionClose:
		s.push(input{kind: inToDevice, dev: deviceID, env: env})
	case *cadencev2.Envelope_StreamStart:
		if b.StreamStart.GetPurpose() == cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY {
			s.push(input{kind: inToDevice, dev: deviceID, env: env})
		}
	}
}

// Closed hears that a device's link closed.
func (s *Service) Closed(deviceID string) {
	s.d.Contexts.Forget(deviceID)
	s.push(input{kind: inClosed, dev: deviceID})
}

// Linked hears that a device's link opened: a screen of a room whose turn is running joins it with each card's
// latest state, as does one back after a drop.
func (s *Service) Linked(deviceID string) {
	s.push(input{kind: inLinked, dev: deviceID})
}

// CallKey names a tool call of a turn as its step does: Attach's callKey.
func CallKey(turnID, callID string) string { return turnID + ":" + callID }

// Attach shows subj on the screens of the turn of the device's session (a
// search tool's results) as the card of the call callKey names (CallKey; ""
// for none); nothing when no screen shows the turn.
func (s *Service) Attach(deviceID, sessionID, callKey string, subj cards.Subject) {
	if subj == nil || sessionID == "" {
		return
	}
	s.push(input{kind: inAttach, dev: deviceID, session: sessionID, key: callKey, subj: subj})
}

// Transcript takes a hypothesis of a session's utterance as STT gives it
// (voice.Deps.Transcript): every partial goes to the turn's screens as it
// comes, then the final. The room's screens show it whichever device listens.
func (s *Service) Transcript(deviceID, sessionID, text string, final bool) {
	if sessionID == "" || text == "" {
		return
	}
	s.push(input{kind: inHeard, dev: deviceID, session: sessionID, text: text, final: final})
}

// busTypes are the agent's events a turn on a screen shows
var busTypes = map[string]bool{"transcript": true, "tool_call": true, "tool_result": true, "llm_text": true}

// Run follows bus until ctx ends.
func (s *Service) Run(ctx context.Context, bus *events.Bus) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	var sub *events.Subscription
	if bus != nil {
		sub = bus.Watch(func(e events.Event) bool { return busTypes[e.Type] && e.SessionID != "" })
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.loop(ctx, sub)
	}()
	return func() {
		cancel()
		wg.Wait()
		if sub != nil {
			sub.Close()
		}
	}
}

func (s *Service) loop(ctx context.Context, sub *events.Subscription) {
	var evs <-chan events.Event
	if sub != nil {
		evs = sub.C
	}
	sweep := time.NewTicker(time.Minute)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case in := <-s.in:
			s.handle(ctx, in)
		case e, ok := <-evs:
			if !ok {
				evs = nil
				continue
			}
			s.handle(ctx, input{kind: inBus, dev: e.DeviceID, ev: e})
		case <-sweep.C:
			s.sweepTurns()
			s.d.Contexts.Sweep()
		}
	}
}

func (s *Service) handle(ctx context.Context, in input) {
	defer func() {
		// the value can carry anything, so only the device is logged
		if recover() != nil {
			s.d.Log.Error("screens_panic", "device_id", in.dev)
		}
	}()
	switch in.kind {
	case inFromDevice:
		s.fromDevice(ctx, in.dev, in.env)
	case inToDevice:
		s.toDevice(ctx, in.dev, in.env)
	case inBus:
		s.busEvent(ctx, in.ev)
	case inAttach:
		if t := s.turns[in.session]; t != nil && t.origin == in.dev {
			s.attach(ctx, t, in.key, in.subj)
			s.send(ctx, t)
		}
	case inHeard:
		s.heard(ctx, in.dev, in.session, in.text, in.final)
	case inStop:
		in.stopped <- s.stopTarget(in.dev, in.session)
	case inReset:
		s.resetConversation(ctx, in.dev, in.session)
	case inResetTarget:
		in.target <- s.resetTarget(in.dev)
	case inLinked:
		s.joined(ctx, in.dev)
	case inClosed:
		for id, t := range s.turns {
			if t.origin == in.dev {
				t.state, t.errorCode = cadencev2.AssistantState_ASSISTANT_STATE_DISMISSED, "offline"
				s.end(ctx, id, t)
			}
		}
	}
}

func (s *Service) sweepTurns() {
	now := s.d.Now()
	for id, t := range s.turns {
		if now.Sub(t.started) > turnTTL {
			s.end(context.Background(), id, t)
		}
	}
	s.pmu.Lock()
	for id, p := range s.pending {
		if now.Sub(p.at) > pendingTTL {
			delete(s.pending, id)
		}
	}
	s.pmu.Unlock()
}

func (s *Service) takePending(session string) (pendingOpen, bool) {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	p, ok := s.pending[session]
	delete(s.pending, session)
	return p, ok && s.d.Now().Sub(p.at) <= pendingTTL
}

func (s *Service) lang(ctx context.Context, dev string) string {
	if s.d.Lang == nil {
		return ""
	}
	return s.d.Lang(ctx, dev)
}

// CallCards answers a tool call's step line and cards as far as it went (cards.ToolCards).
type CallCards interface {
	Title(m cards.Messages, lang string, c cards.Call) string
	CallSubjects(c cards.Call) ([]cards.Subject, string)
}

// stepLabel is a call's step line in the screen's language
func (s *Service) stepLabel(lang string, c cards.Call) string {
	if s.d.Calls != nil {
		return s.d.Calls.Title(s.d.Messages, lang, c)
	}
	return (&cards.ToolCards{}).Title(s.d.Messages, lang, c)
}

func (s *Service) setPressed(session, screen string) {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	if screen == "" {
		delete(s.pressed, session)
		return
	}
	s.pressed[session] = screen
}

// PressedBy is the screen whose assistant button opened the session while it runs, "" for none: the screen the
// user is looking at, which a screen tool's target prefers.
func (s *Service) PressedBy(session string) string {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	return s.pressed[session]
}
