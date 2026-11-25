package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const (
	device  = "11111111-2222-3333-4444-555555555555"
	session = "session-1"
)

var turnStart = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

var (
	fastMode = agent.Mode{
		Name: "fast", Model: "model-fast-x", Reasoning: "none", MaxSteps: 8, MaxDuration: 20 * time.Second,
		Interim: agent.InterimDrop,
	}
	smartMode = agent.Mode{
		Name: "smart", Reasoning: "medium", MaxSteps: 25, MaxDuration: 90 * time.Second, Interim: agent.InterimSpeak,
	}
)

// llmStep answers one request of a turn; the last step repeats. Its text
// streams in two fragments, cut after the first space.
type llmStep struct {
	text    string
	calls   []providers.ToolCall
	fail    bool            // an Err chunk after the text
	noStart bool            // no ToolCallStart before the calls
	hold    <-chan struct{} // closed to let the stream go on after its first fragment
	block   bool            // after the text, wait for the request's ctx to end
	usage   *providers.Usage
	noUsage bool // the stream reports no usage
}

func toolCall(id, name, args string) providers.ToolCall {
	return providers.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

type scriptLLM struct {
	steps   []llmStep
	inner   providers.LLM // when set, answers instead of steps
	openErr bool

	mu   sync.Mutex
	reqs []providers.LLMRequest
	at   []time.Time // when each request came, by the wall clock
}

func (l *scriptLLM) Stream(ctx context.Context, r providers.LLMRequest) (<-chan providers.LLMChunk, error) {
	l.mu.Lock()
	r.Messages = slices.Clone(r.Messages)
	l.reqs = append(l.reqs, r)
	l.at = append(l.at, time.Now())
	n := len(l.reqs)
	l.mu.Unlock()
	if l.openErr {
		return nil, errors.New(providers.CodeLLMFailed)
	}
	if l.inner != nil {
		return l.inner.Stream(ctx, r)
	}
	s := l.steps[min(n, len(l.steps))-1]
	ch := make(chan providers.LLMChunk)
	go func() {
		defer close(ch)
		emit := func(c providers.LLMChunk) bool {
			select {
			case ch <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for i, part := range strings.SplitAfterN(s.text, " ", 2) {
			if part != "" && !emit(providers.LLMChunk{Text: part}) {
				return
			}
			if i == 0 && s.hold != nil {
				select {
				case <-s.hold:
				case <-ctx.Done():
					return
				}
			}
		}
		if s.block {
			// as a provider does, the stream ends without an error once its ctx is done
			<-ctx.Done()
			return
		}
		if s.fail {
			emit(providers.LLMChunk{Err: errors.New(providers.CodeLLMFailed)})
			return
		}
		if len(s.calls) > 0 {
			if !s.noStart && !emit(providers.LLMChunk{ToolCallStart: true}) {
				return
			}
			if !emit(providers.LLMChunk{ToolCalls: slices.Clone(s.calls)}) {
				return
			}
		}
		if s.noUsage {
			return
		}
		u := s.usage
		if u == nil {
			u = &providers.Usage{PromptTokens: 100, CompletionTokens: 10}
		}
		emit(providers.LLMChunk{Usage: u})
	}()
	return ch, nil
}

func (l *scriptLLM) requests() []providers.LLMRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.reqs)
}

func (l *scriptLLM) times() []time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.at)
}

type llmSource struct {
	llm   providers.LLM
	err   error
	asked []string
}

func (s *llmSource) LLM(_ context.Context, name string) (providers.LLM, error) {
	s.asked = append(s.asked, name)
	return s.llm, s.err
}

type modeTable map[string]agent.Mode

func (m modeTable) Get(_ context.Context, name string) (agent.Mode, error) {
	mode, ok := m[name]
	if !ok {
		return agent.Mode{}, agent.ErrModeNotFound
	}
	return mode, nil
}

type historyAsk struct {
	device   string
	after    int64
	maxTurns int
}

type appended struct {
	device, turn string
	meta         conversation.TurnMeta
	msgs         []providers.Message
}

type convStore struct {
	history []providers.Message
	// hold, when set, keeps Append waiting until it closes
	hold chan struct{}
	// stall keeps History waiting until its ctx ends
	stall bool
	// failAppend makes every commit fail
	failAppend bool
	mu         sync.Mutex
	asked      []historyAsk
	stored     []appended
}

func (c *convStore) History(ctx context.Context, deviceID string, afterID int64, maxTurns int) ([]providers.Message, error) {
	if c.stall {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, historyAsk{deviceID, afterID, maxTurns})
	return slices.Clone(c.history), nil
}

func (c *convStore) Append(_ context.Context, deviceID, turnID string, meta conversation.TurnMeta, msgs []providers.Message) error {
	if c.hold != nil {
		<-c.hold
	}
	if c.failAppend {
		return errors.New("append failed")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stored = append(c.stored, appended{deviceID, turnID, meta, slices.Clone(msgs)})
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// ran records a fake tool's calls.
type ran struct {
	mu    sync.Mutex
	calls []agent.Call
}

func (r *ran) add(c agent.Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *ran) all() []agent.Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

const objectInput = `{"type":"object"}`

const levelInput = `{"type":"object","properties":{"level":{"type":"integer","minimum":0,"maximum":100}},` +
	`"required":["level"],"additionalProperties":false}`

// fakeTool answers result and records its calls; before runs first, when set.
func fakeTool(name string, effects agent.Effect, output agent.Trust, result string, before func()) (agent.Tool, *ran) {
	r := &ran{}
	return agent.Tool{
		Name: name, Description: name + " tool", Input: objectInput, Effects: effects, Output: output,
		Run: func(_ context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			if before != nil {
				before()
			}
			r.add(c)
			return json.RawMessage(result), nil
		},
	}, r
}

func volumeTool() (agent.Tool, *ran) {
	t, r := fakeTool("device_volume", agent.Write, agent.Trusted, `{"level":20}`, nil)
	t.Input, t.SilentOnSuccess = levelInput, true
	return t, r
}

type harness struct {
	t           *testing.T
	llm         *scriptLLM
	src         *llmSource
	values      settings.Values
	modes       modeTable
	state       *agent.ModeState
	convs       *convStore
	bus         *events.Bus
	feed        <-chan events.Event
	clock       *clock
	logs        *lockedBuffer
	volume      json.RawMessage // audio.output state; nil: unknown
	alarms      json.RawMessage // alarms state; nil: none
	afterRing   *agent.AfterRing
	tools       []agent.Tool
	sources     []testSource // added after tools, in order
	out         chan agent.Out
	settingsErr error
	limits      []time.Duration      // first chunk, reserve, forced bound; nil: the defaults
	history     time.Duration        // the history read's bound; 0: the default
	memories    agent.Memories       // nil: no recall
	recall      time.Duration        // the recall's bound; 0: the default
	summaries   agent.Summaries      // nil: no compactions
	tasks       agent.Tasks          // nil: no background tasks
	tasksBound  time.Duration        // the task read's bound; 0: the default
	taskID      string               // the task an event turn is about
	home        homeNotes            // nil: no home note
	inbox       inboxNotes           // nil: no unread updates block
	homeBound   time.Duration        // the home note's bound; 0: the default
	household   agent.Household      // nil: no devices note
	screens     agent.ScreenContexts // nil: no screen note
	catalog     agent.Catalog
	skills      agent.Skills // nil: no skills
	reset       func(ctx context.Context, deviceID string) error
}

// inboxNotes is the runner's unread updates dependency.
type inboxNotes = func(ctx context.Context, deviceID, turnID string) (string, error)

// homeNotes is the runner's home note dependency.
type homeNotes = func(ctx context.Context, deviceID string, v settings.Values) (string, error)

func newHarness(t *testing.T, steps ...llmStep) *harness {
	llm := &scriptLLM{steps: steps}
	v := settings.Defaults()
	v.LLM, v.Model = "llm-instance-x", "device-model-x"
	h := &harness{
		t: t, llm: llm, src: &llmSource{llm: llm}, values: v,
		modes: modeTable{"fast": fastMode, "smart": smartMode}, state: agent.NewModeState(),
		convs: &convStore{}, bus: events.NewBus(1024), clock: &clock{t: turnStart}, logs: &lockedBuffer{},
		out: make(chan agent.Out, 256),
	}
	var stop func()
	h.feed, stop = h.bus.Subscribe(nil)
	t.Cleanup(stop)
	return h
}

func (h *harness) runner() *agent.Runner {
	reg := agent.NewRegistry(h.tools...)
	for _, s := range h.sources {
		reg.AddSource(s.name, s.src)
	}
	r := agent.NewRunner(agent.RunnerDeps{
		Providers: h.src,
		Settings: func(_ context.Context, id string) (settings.Values, error) {
			if id != device || h.settingsErr != nil {
				return settings.Values{}, errors.New("unknown device")
			}
			return h.values, nil
		},
		Modes: h.modes, ModeState: h.state, Tools: reg, Conversations: h.convs, Bus: h.bus, Memories: h.memories,
		Summaries: h.summaries, Tasks: h.tasks, Home: h.home, Inbox: h.inbox,
		ResetConversation: h.reset,
		Household:         h.household, Screens: h.screens, Catalog: h.catalog, Skills: h.skills,
		State: func(_ context.Context, id, capability string) (json.RawMessage, bool) {
			if id == device && capability == "alarms" && h.alarms != nil {
				return h.alarms, true
			}
			if id != device || capability != "audio.output" || h.volume == nil {
				return nil, false
			}
			return h.volume, true
		},
		Now: h.clock.Now, Log: slog.New(slog.NewJSONHandler(h.logs, nil)),
	})
	if h.limits != nil {
		agent.SetLimits(r, h.limits[0], h.limits[1], h.limits[2])
	}
	if h.recall > 0 {
		agent.SetRecallBound(r, h.recall)
	}
	if h.history > 0 {
		agent.SetHistoryBound(r, h.history)
	}
	if h.tasksBound > 0 {
		agent.SetTasksBound(r, h.tasksBound)
	}
	if h.homeBound > 0 {
		agent.SetHomeBound(r, h.homeBound)
	}
	return r
}

func (h *harness) turn(trigger cadencev2.Trigger, text string) agent.Turn {
	return agent.Turn{DeviceID: device, SessionID: session, Trigger: trigger, Text: text, Language: "en", TaskID: h.taskID,
		AfterRing: h.afterRing}
}

// run answers text in a session of trigger and returns what was said.
func (h *harness) run(trigger cadencev2.Trigger, text string) (agent.Result, []agent.Out) {
	return h.runIn(context.Background(), trigger, text)
}

func (h *harness) runIn(ctx context.Context, trigger cadencev2.Trigger, text string) (agent.Result, []agent.Out) {
	h.t.Helper()
	res := h.runner().Run(ctx, h.turn(trigger, text), h.out)
	outs, ended := h.drain()
	// a turn whose ctx ended may not get its End out
	if !ended && ctx.Err() == nil {
		h.t.Fatalf("speech %+v without its End", outs)
	}
	return res, outs
}

// said drains what the turn sent so far, without its End.
func (h *harness) said() []agent.Out {
	h.t.Helper()
	outs, _ := h.drain()
	return outs
}

// drain takes what the turn sent so far. End must come once and last; it
// is left out and reported.
func (h *harness) drain() ([]agent.Out, bool) {
	h.t.Helper()
	var outs []agent.Out
	for len(h.out) > 0 {
		outs = append(outs, <-h.out)
	}
	n := len(outs)
	ended := n > 0 && outs[n-1] == (agent.Out{End: true})
	if ended {
		outs = outs[:n-1]
	}
	for _, o := range outs {
		if o.End {
			h.t.Fatalf("speech %+v has an End before its last fragment", outs)
		}
	}
	return outs, ended
}

// requests returns the turn's llm requests, failing unless there were n.
func (h *harness) requests(n int) []providers.LLMRequest {
	h.t.Helper()
	reqs := h.llm.requests()
	if len(reqs) != n {
		h.t.Fatalf("%d llm calls, want %d", len(reqs), n)
	}
	return reqs
}

func (h *harness) events() []events.Event {
	var out []events.Event
	for {
		select {
		case e := <-h.feed:
			out = append(out, e)
		default:
			return out
		}
	}
}

func eventsOf(all []events.Event, typ string) []map[string]any {
	var out []map[string]any
	for _, e := range all {
		if e.Type == typ {
			out = append(out, e.Data.(map[string]any))
		}
	}
	return out
}

func toolNames(specs []providers.ToolSpec) []string {
	out := []string{}
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

func last(msgs []providers.Message) providers.Message { return msgs[len(msgs)-1] }

func wantOuts(t *testing.T, got []agent.Out, want ...agent.Out) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("speech %+v, want %+v", got, want)
	}
}

// wantSpeech checks that the turn said text, in any fragments, discarding nothing.
func wantSpeech(t *testing.T, got []agent.Out, text string) {
	t.Helper()
	var said strings.Builder
	for _, o := range got {
		if o.Discard {
			t.Fatalf("speech %+v discards", got)
		}
		said.WriteString(o.Speech)
	}
	if said.String() != text {
		t.Fatalf("said %q, want %q", said.String(), text)
	}
}

// speak is the fragments the scripted llm streams for text.
func speak(text string) []agent.Out {
	var out []agent.Out
	for _, p := range strings.SplitAfterN(text, " ", 2) {
		if p != "" {
			out = append(out, agent.Out{Speech: p})
		}
	}
	return out
}
