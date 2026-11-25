package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// turn codes besides the providers' own
const (
	CodeNotConfigured = "not_configured"
	CodeInternal      = "internal"
	CodeTimeout       = "timeout"
	CodeCanceled      = "canceled"
)

const (
	roleSystem    = "system"
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"
)

const (
	fastMode = "fast"
	// a ceiling, not the reply's length (the prompt keeps replies short): models behind a gateway may think even with
	// reasoning off, and thinking counts against max_tokens, so a tight cap ended turns mid-thought with no reply
	maxTokens = 1024
	// reasoning tokens count against max_tokens (openrouter)
	reasoningMaxTokens = 4096
	commitTimeout      = 5 * time.Second
)

type limits struct {
	firstChunk time.Duration // a stream silent this long has stalled
	reserve    time.Duration // kept before the caller's deadline for the forced call
	forced     time.Duration // bound of the forced call
	history    time.Duration // bound of the history read; past it the turn runs without
	recall     time.Duration // bound of the memory recall; past it the turn runs without
	tasks      time.Duration // bound of the task read; past it the turn runs without
	inbox      time.Duration // bound of the unread updates read; past it the turn runs without
	home       time.Duration // bound of the home note; past it the turn runs without
	devices    time.Duration // bound of the household read; past it the turn knows only its own device
}

var defaultLimits = limits{
	firstChunk: 10 * time.Second, reserve: 8 * time.Second, forced: 8 * time.Second, history: 2 * time.Second,
	recall: 1500 * time.Millisecond, tasks: time.Second, inbox: time.Second, home: 300 * time.Millisecond,
	devices: devicesBound,
}

// Turn is one utterance to answer. ID is the caller's stable id for the stored
// turn (voice uses its session id); empty makes a fresh random one. Settings
// and Mode, when set, are what the caller already read for the turn (the mode
// from PeekMode), so the runner does not read them again. TaskID is the short
// id of the task whose event starts an event turn; its tools may act on that
// task alone. Typed is a turn the owner typed in the dashboard: its Trigger
// decides what it may do, and it is published and stored with the trigger
// dashboard.
type Turn struct {
	ID                  string
	DeviceID, SessionID string
	Trigger             cadencev2.Trigger
	Typed               bool
	Text, Language      string
	TaskID              string
	Settings            *settings.Values
	Mode                *Mode
	// AfterRing is the ring the wake that began the session stopped; nil for none
	AfterRing *AfterRing
}

// Out is the next fragment of speech, as the model writes it. Discard drops
// the speech of the current step that was not played yet: text written
// before the model turned to tools in drop mode, or a step the budget cut.
// End comes last, once nothing more will be said, and is no speech itself:
// it goes out before the turn is committed, so speech never waits on the
// store (M3-R11).
type Out struct {
	Speech  string
	Discard bool
	End     bool
}

// Result ends a turn: Code "" is ok, and Silent means nothing was said.
// Failure is the codes-only detail of a failed Code, for the voice session to
// forward to screens and, when tts fails later, replace.
type Result struct {
	Code    string
	Silent  bool
	Failure *conversation.TurnFailure
}

// RunnerDeps are the runner's collaborators.
type RunnerDeps struct {
	Providers interface {
		LLM(ctx context.Context, name string) (providers.LLM, error)
	}
	Settings func(ctx context.Context, deviceID string) (settings.Values, error)
	Modes    interface {
		Get(ctx context.Context, name string) (Mode, error)
	}
	ModeState     *ModeState
	Tools         *Registry
	Conversations interface {
		// History is the raw turns committed after the message afterID, the newest maxTurns.
		History(ctx context.Context, deviceID string, afterID int64, maxTurns int) ([]providers.Message, error)
		// Append commits a turn: its row (meta) and its messages, all or none.
		Append(ctx context.Context, deviceID, turnID string, meta conversation.TurnMeta, msgs []providers.Message) error
	}
	// ResetConversation moves the device's context boundary after a turn whose
	// conversation_reset call succeeded. Nil leaves the tool without an effect.
	ResetConversation func(ctx context.Context, deviceID string) error
	// Memories recalls the memories a turn reads; nil leaves them out.
	Memories Memories
	// Summaries holds the conversation summaries; nil leaves them out and
	// takes the raw turns from the start.
	Summaries Summaries
	// Tasks lists the background tasks the turn shows; nil leaves them out.
	Tasks Tasks
	// Inbox reads the unread updates block of a woken turn by its id (the
	// subscriptions hub's), marking them read; nil leaves it out. The block
	// is outside content, so a turn that gets one is tainted.
	Inbox func(ctx context.Context, deviceID, turnID string) (string, error)
	// Home builds the home note (HomeNotes) for a turn offered ha_control;
	// nil leaves it out. It runs beside the history read and must answer once
	// its ctx ends; a note may come with an error, which is logged by code.
	Home func(ctx context.Context, deviceID string, v settings.Values) (string, error)
	// Playing is what the speaker plays (the now-playing note); nil leaves
	// the note out. It reads memory only, so it answers at once.
	Playing func(deviceID string) (NowPlaying, bool)
	// Household lists the household's devices for the devices note and the
	// tools' reach; nil leaves the note out and holds no tool back.
	Household Household
	// Screens reads a screen's context and Catalog names its refs, for the
	// screen note; nil Screens leaves the note out, nil Catalog shows refs
	// without their titles.
	Screens ScreenContexts
	Catalog Catalog
	// Skills lists and picks a turn's skills; nil leaves them and load_skill
	// out
	Skills Skills
	Bus    *events.Bus
	// State reads a capability's last reported state.
	State func(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)
	Now   func() time.Time
	Log   *slog.Logger
}

// Runner runs voice turns through the agent loop (spec §3).
type Runner struct {
	d   RunnerDeps
	lim limits

	homeMu sync.Mutex
	// homeLast is the last home note's failure code, "" for none
	homeLast string
}

func NewRunner(d RunnerDeps) *Runner {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Tools == nil {
		d.Tools = NewRegistry()
	}
	if d.State == nil {
		d.State = func(context.Context, string, string) (json.RawMessage, bool) { return nil, false }
	}
	return &Runner{d: d, lim: defaultLimits}
}

type failure struct{ tool, code string }

type turn struct {
	Turn
	id    string
	start time.Time
	v     settings.Values
	devs  Devices
	mode  Mode
	lang  string
	llm   providers.LLM
	// toolset is the registry's tools as the turn began, which it offers,
	// calls and redacts its history by
	toolset []Tool
	// msgs is what the llm reads; kept is what an ok turn commits, the user message first
	msgs, kept []providers.Message

	steps, calls int
	tools        []string // registered tools called, for the log
	usage        providers.Usage
	metered      bool // a model call reported its usage
	tainted      bool
	expired      bool // the time budget ran out
	// held is the time steps spent in Holds calls, outside the mode's time
	held          time.Duration
	said          bool // an Out went to the device
	lastOffered   []Tool
	removed       map[string]bool
	failed        map[failure]int
	historyFailed bool
	tasksFailed   bool
	// skills is what the turn listed and loaded, nil for none
	skills      *turnSkills
	inboxFailed bool
	// reset: conversation_reset succeeded; finish moves the boundary after this turn
	reset bool
	// failure is the last failed call's detail; a later successful forced answer ignores it
	failure *conversation.TurnFailure
	// spoken is all the speech that went out and was not discarded since;
	// stepMark is where the current step's began
	spoken   string
	stepMark int
	// stepSpan is the running step's agent.step, ended by logStep
	stepSpan trace.Span
}

// Run answers t and sends its speech to out in order; it never closes out.
// Text, arguments and results go to the live bus and the conversation store
// only: the log gets ids, counts, codes, tool names, tokens and durations.
func (r *Runner) Run(ctx context.Context, t Turn, out chan<- Out) Result {
	res, _ := r.run(ctx, t, nil, out)
	return res
}

// run is Run with a seed, a tool call the server made for the turn before it
// began, and it also returns all the turn said, every step's speech that was
// not discarded.
func (r *Runner) run(ctx context.Context, t Turn, sd *seed, out chan<- Out) (res Result, spoken string) {
	id := t.ID
	if id == "" {
		id = rand.Text()
	}
	tr := &turn{
		Turn: t, id: id, start: r.d.Now(), removed: map[string]bool{}, failed: map[failure]int{},
		kept: []providers.Message{{Role: roleUser, Content: t.Text}},
	}
	// the trigger by the name the history stores (ruling M7bs-R6); none for an
	// unspecified one
	start := map[string]any{}
	if name := triggerName(t); name != "" {
		start["trigger"] = name
	}
	r.publish(tr, "turn_start", start)
	r.publish(tr, "transcript", map[string]any{"text": t.Text})
	ctx, span := startTurnSpan(ctx, tr)
	defer func() {
		// the value can carry anything, so it is dropped
		if recover() != nil {
			res = Result{Code: CodeInternal}
		}
		defer endTurnSpan(span, tr, res)
		select {
		case out <- Out{End: true}:
		case <-ctx.Done():
		}
		res.Failure = r.finish(ctx, tr, res)
		if res.Code == "" {
			spoken = tr.spoken
		}
	}()
	r.plant(tr, sd)
	if code := r.prepareSpan(ctx, tr); code != "" {
		return Result{Code: code}, ""
	}
	return r.loop(ctx, tr, out), ""
}

// prepare takes the turn's tool snapshot, resolves its settings, mode and llm
// and assembles the context in the order of spec §5.
func (r *Runner) prepare(ctx context.Context, tr *turn) string {
	tr.toolset = r.d.Tools.snapshot(r.d.Log)
	var v settings.Values
	var err error
	if tr.Settings != nil {
		v = *tr.Settings
	} else if v, err = r.d.Settings(ctx, tr.DeviceID); err != nil {
		return orCtx(ctx, CodeInternal)
	}
	tr.v = v
	tr.devs = r.readDevices(ctx, tr.DeviceID)
	// counts the turn as the mode's use even when the caller brought the mode;
	// an event turn is nobody's activity, so it only reads the mode
	name := r.d.ModeState.Current
	if EventTurn(tr.Trigger) {
		name = r.d.ModeState.Peek
	}
	mode := name(tr.DeviceID, v, tr.start)
	if tr.Mode != nil && tr.Mode.Name == mode {
		tr.mode = *tr.Mode
	} else if tr.mode, err = r.loadMode(ctx, mode, v); err != nil {
		if errors.Is(err, ErrModeNotFound) {
			return CodeNotConfigured
		}
		return orCtx(ctx, CodeInternal)
	}
	if v.LLM == "" {
		return CodeNotConfigured
	}
	if tr.llm, err = r.d.Providers.LLM(ctx, v.LLM); err != nil {
		if errors.Is(err, providers.ErrNotFound) || errors.Is(err, providers.ErrWrongKind) ||
			errors.Is(err, providers.ErrUnknownModule) {
			return CodeNotConfigured
		}
		return orCtx(ctx, providers.CodeLLMFailed)
	}
	recalled := r.startRecall(ctx, tr)
	home := r.startHome(ctx, tr)
	hctx, cancel := context.WithTimeout(ctx, r.lim.history)
	summary, history, err := r.readHistory(hctx, tr.DeviceID, v.HistoryTurns)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return ctxCode(ctx)
		}
		// the turn still answers, only without what came before
		tr.historyFailed, summary, history = true, nil, nil
	}
	board, failed := r.readTasks(ctx)
	if failed && ctx.Err() != nil {
		return ctxCode(ctx)
	}
	tr.tasksFailed = failed
	inbox := r.readInbox(ctx, tr)
	lang := tr.Language
	if lang == "" {
		lang = v.Language
	}
	state, known := r.d.State(ctx, tr.DeviceID, "audio.output")
	tr.lang = lang
	// the pinned memories change rarely, so they stay in the cached prefix;
	// the recalled ones follow the input, and the home its live state, so
	// they come late (M5a-R5, M5c-R2)
	pinned, matched, rec := r.collectRecall(tr, recalled)
	index, skills := r.prepareSkills(tr, SkillQuery{Input: tr.Text, Vector: rec.Vector, Model: rec.Model, Embed: v.Embed})
	tr.msgs = append(tr.msgs, providers.Message{Role: roleSystem, Content: systemPrompt(v.Persona, lang, tr.mode.Interim)})
	// the skills listed change only with the skills and the tools offered,
	// so they stay in the cached prefix; the loaded ones follow the input
	tr.msgs = appendNote(tr.msgs, index)
	tr.msgs = appendNote(tr.msgs, memoriesNote(pinned))
	tr.msgs = appendNote(tr.msgs, summaryNote(summary))
	tr.msgs = append(tr.msgs, redactHistory(tr.toolset, history)...)
	tr.msgs = appendNote(tr.msgs, memoriesNote(matched))
	tr.msgs = appendNote(tr.msgs, home())
	tr.msgs = appendNote(tr.msgs, board)
	tr.msgs = appendNote(tr.msgs, inbox)
	if !EventTurn(tr.Trigger) {
		tr.msgs = appendNote(tr.msgs, devicesNote(tr.devs))
		tr.msgs = appendNote(tr.msgs, r.screenNotes(tr.devs))
	}
	if r.d.Playing != nil && !EventTurn(tr.Trigger) {
		tr.msgs = appendNote(tr.msgs, nowPlayingNote(r.d.Playing(tr.DeviceID)))
	}
	tr.msgs = appendNote(tr.msgs, skills)
	tr.msgs = appendNote(tr.msgs, r.afterRingNote(ctx, tr.DeviceID, tr.AfterRing))
	tr.msgs = append(tr.msgs, providers.Message{Role: roleSystem, Content: volatileNote(tr.start, v.TimeZone, state, known)})
	// the input, and a seed's call and output after it
	tr.msgs = append(tr.msgs, tr.kept...)
	return ""
}

// appendNote adds a system note unless it is empty.
func appendNote(msgs []providers.Message, note string) []providers.Message {
	if note == "" {
		return msgs
	}
	return append(msgs, providers.Message{Role: roleSystem, Content: note})
}

// PeekMode is the mode the device's next turn runs, false when none loads.
// It does not count as the mode's use; the turn does.
func (r *Runner) PeekMode(ctx context.Context, deviceID string, v settings.Values) (Mode, bool) {
	m, err := r.loadMode(ctx, r.d.ModeState.Peek(deviceID, v, r.d.Now()), v)
	return m, err == nil
}

// loadMode loads the turn's mode. One that does not load (a mode set by voice
// whose row is gone) falls back to the mode setting, then fast: failing the
// turn would fail every turn while the voiced mode holds.
func (r *Runner) loadMode(ctx context.Context, name string, v settings.Values) (Mode, error) {
	var tried []string
	var err error
	for _, n := range []string{name, settingMode(v), fastMode} {
		if slices.Contains(tried, n) {
			continue
		}
		tried = append(tried, n)
		var m Mode
		if m, err = r.d.Modes.Get(ctx, n); err == nil || ctx.Err() != nil {
			return m, err
		}
	}
	return Mode{}, err
}

// loop runs normal steps under the time budget until the model answers, a
// silent step ends the turn or a budget runs out and forces the answer.
func (r *Runner) loop(ctx context.Context, tr *turn, out chan<- Out) Result {
	for {
		stepStart := r.d.Now()
		tools := r.offered(tr)
		if len(tools) > 0 {
			tr.lastOffered = tools
		}
		if spent := r.spent(ctx, tr); spent != "" {
			return r.forced(ctx, tr, tools, spent, out)
		}
		// the step's span rides a child of ctx: same end, so the turn's checks read it alike
		ctx := r.startStep(ctx, tr, len(tools), false)
		sctx, cancel := context.WithTimeout(ctx, r.stepTimeout(ctx, tr))
		rep := r.stream(ctx, sctx, tr, tools, false, out)
		switch {
		case rep.code == codeExpired:
			cancel()
			r.logStep(tr, stepStart, rep, nil)
			// the cut step is dropped, and so is what it said
			tr.expired = true
			if rep.sent > 0 && !rep.discarded && !r.say(ctx, tr, out, Out{Discard: true}) {
				return Result{Code: ctxCode(ctx)}
			}
			continue
		case rep.code != "":
			cancel()
			r.logStep(tr, stepStart, rep, nil)
			return Result{Code: rep.code}
		case len(rep.calls) == 0:
			cancel()
			r.logStep(tr, stepStart, rep, nil)
			return r.answer(ctx, tr, rep, out)
		}
		if !r.endSpokenStep(ctx, tr, rep, out) {
			cancel()
			r.logStep(tr, stepStart, rep, nil)
			return Result{Code: ctxCode(ctx)}
		}
		cctx, release := r.callsContext(ctx, sctx, tools, rep)
		callsStart := r.d.Now()
		done := r.runCalls(cctx, tr, tools, rep)
		tr.expired = tr.expired || errors.Is(cctx.Err(), context.DeadlineExceeded)
		if release != nil {
			release()
			tr.held += r.d.Now().Sub(callsStart)
		}
		cancel()
		r.logStep(tr, stepStart, rep, done)
		if ctx.Err() != nil {
			contextToolOutcome(ctx, tr, done)
			return Result{Code: ctxCode(ctx)}
		}
		// spec §3.8 as S0 has it: the action speaks for itself, so no further call
		if allSilent(done) {
			return Result{Silent: !tr.said}
		}
		r.followMode(ctx, tr)
	}
}

// forced makes the one last call a spent budget allows, under its own bound.
func (r *Runner) forced(ctx context.Context, tr *turn, tools []Tool, spent string, out chan<- Out) Result {
	stepStart := r.d.Now()
	r.publish(tr, "budget_spent", map[string]any{"steps": tr.steps, "reason": spent})
	tr.msgs = append(tr.msgs, providers.Message{Role: roleSystem, Content: budgetNote})
	ctx = r.startStep(ctx, tr, len(tools), true)
	fctx, cancel := context.WithTimeout(ctx, r.lim.forced)
	defer cancel()
	rep := r.stream(ctx, fctx, tr, tools, true, out)
	r.logStep(tr, stepStart, rep, nil)
	switch rep.code {
	case "":
		return r.answer(ctx, tr, rep, out)
	case codeExpired:
		return Result{Code: CodeTimeout}
	default:
		return Result{Code: rep.code}
	}
}

// offered is the step's tool list from the turn's snapshot: mode ∩ device
// setting (a source's or a DefaultOff tool: the device setting alone), Read and
// taint-exempt only once tainted (in an event turn, Read and the tools marked
// for it), without the tools removed for failing twice the same way or that
// the device's settings leave without what they need.
func (r *Runner) offered(tr *turn) []Tool {
	tools := offer(tr.toolset, tr.mode.Tools, tr.v.Tools, tr.tainted, tr.Trigger)
	return slices.DeleteFunc(tools, func(t Tool) bool {
		if t.turnScoped && t.Name == LoadSkill && (tr.skills == nil || tr.skills.all()) {
			return true
		}
		return tr.removed[t.Name] || (t.Available != nil && !t.Available(tr.v)) || (t.Reach != nil && !t.Reach(tr.devs))
	})
}

// stepTimeout is what a normal step may take: the rest of the mode's
// seconds, and never into the reserve the forced call needs before the
// caller's deadline.
func (r *Runner) stepTimeout(ctx context.Context, tr *turn) time.Duration {
	d := tr.mode.MaxDuration - (r.d.Now().Sub(tr.start) - tr.held)
	if dl, ok := ctx.Deadline(); ok {
		d = min(d, time.Until(dl)-r.lim.reserve)
	}
	return d
}

// callsContext is what a step's calls run under: the step's own context,
// unless one calls a Holds tool, which gets its Timeout (at most MaxHold)
// past the step's end, short of the reserve before the turn's deadline. The
// release is nil for the step's own.
func (r *Runner) callsContext(ctx, sctx context.Context, tools []Tool, rep reply) (context.Context, func()) {
	var hold time.Duration
	for _, c := range rep.calls {
		if t, ok := find(tools, c.Name); ok && t.Holds {
			hold = max(hold, min(t.Timeout, MaxHold))
		}
	}
	if hold <= 0 {
		return sctx, nil
	}
	if dl, ok := sctx.Deadline(); ok {
		hold = max(hold, time.Until(dl))
	}
	if dl, ok := ctx.Deadline(); ok {
		hold = min(hold, time.Until(dl)-r.lim.reserve)
	}
	return context.WithTimeout(ctx, hold)
}

// spent names the budget the turn used up, if any; the call it forces is
// one more than MaxSteps.
func (r *Runner) spent(ctx context.Context, tr *turn) string {
	switch {
	case tr.steps >= tr.mode.MaxSteps:
		return "steps"
	case tr.expired || r.stepTimeout(ctx, tr) <= 0:
		return "seconds"
	}
	return ""
}

// answer ends the turn with the model's final text, forwarding whatever of
// it the stream held back. An empty answer is a failed turn, except in an
// event turn, which may choose to say nothing (M5b-D6).
func (r *Runner) answer(ctx context.Context, tr *turn, rep reply, out chan<- Out) Result {
	if strings.TrimSpace(rep.text) == "" {
		if EventTurn(tr.Trigger) {
			return Result{Silent: !tr.said}
		}
		return Result{Code: providers.CodeLLMFailed}
	}
	tr.kept = append(tr.kept, providers.Message{Role: roleAssistant, Content: rep.text})
	rest := rep.text[rep.sent:]
	if rep.discarded {
		rest = rep.text
	}
	if rest != "" && !r.say(ctx, tr, out, Out{Speech: rest}) {
		return Result{Code: ctxCode(ctx)}
	}
	return Result{}
}

// endSpokenStep ends the speech of a step that goes on to its tools with
// whitespace, so the next step's text starts a sentence of its own instead of
// joining this one's last.
func (r *Runner) endSpokenStep(ctx context.Context, tr *turn, rep reply, out chan<- Out) bool {
	if rep.sent == 0 || rep.discarded {
		return true
	}
	if last, _ := utf8.DecodeLastRuneInString(rep.text[:rep.sent]); unicode.IsSpace(last) {
		return true
	}
	return r.say(ctx, tr, out, Out{Speech: "\n"})
}

// followMode applies a mode switched by a tool from the next step on; a
// mode that cannot be loaded leaves the current one.
func (r *Runner) followMode(ctx context.Context, tr *turn) {
	name := r.d.ModeState.Peek(tr.DeviceID, tr.v, r.d.Now())
	if name == tr.mode.Name {
		return
	}
	m, err := r.d.Modes.Get(ctx, name)
	if err != nil {
		return
	}
	r.publish(tr, "mode_switch", map[string]any{"step": tr.steps, "from": tr.mode.Name, "to": m.Name})
	tr.mode = m
	// the rule on text beside tool calls follows the mode's interim policy;
	// cloned, since earlier requests share the array
	tr.msgs = slices.Clone(tr.msgs)
	tr.msgs[0].Content = systemPrompt(tr.v.Persona, tr.lang, m.Interim)
}

// finish commits the turn (completed calls included, so a failed turn's work
// is not retried blindly; nothing of a failed event turn, whose input is our
// own framing and which is tried again), stores what failed, then publishes and
// logs its end.
func (r *Runner) finish(ctx context.Context, tr *turn, res Result) *conversation.TurnFailure {
	meta := r.meta(tr)
	meta.Failure = r.outcome(tr, res)
	msgs := tr.kept
	if res.Code != "" && EventTurn(tr.Trigger) {
		msgs = nil
	}
	stored := true
	if len(msgs) > 0 {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitTimeout)
		cctx, span := tracing.Start(cctx, traceScope, "agent.commit", attribute.Int("cadence.messages", len(msgs)))
		err := r.d.Conversations.Append(cctx, tr.DeviceID, tr.id, meta, msgs)
		tracing.End(span, err)
		stored = err == nil
		cancel()
		if stored {
			r.committed(tr)
		}
	}
	resetFailed := false
	if tr.reset && r.d.ResetConversation != nil {
		// after the commit: the turn that asked is part of the segment it closes
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitTimeout)
		resetFailed = r.d.ResetConversation(rctx, tr.DeviceID) != nil
		cancel()
	}
	code := res.Code
	if code == "" {
		code = "ok"
	}
	ms := r.d.Now().Sub(tr.start).Milliseconds()
	end := map[string]any{
		"code": code, "silent": res.Silent, "steps": tr.steps, "toolCalls": tr.calls,
		"promptTokens": tr.usage.PromptTokens, "completionTokens": tr.usage.CompletionTokens, "ms": ms,
	}
	if meta.Failure != nil {
		end["failure"] = *meta.Failure
	}
	r.publish(tr, "turn_end", end)
	attrs := []any{
		"device_id", tr.DeviceID, "session_id", tr.SessionID, "turn_id", tr.id,
		"steps", tr.steps, "tools_called", tr.calls, "tools", strings.Join(tr.tools, ","), "code", code,
		"prompt_tokens", tr.usage.PromptTokens, "completion_tokens", tr.usage.CompletionTokens, "ms", ms,
	}
	// provider/model ids stay in the DB and dashboard; the log gets codes,
	// the stage and numbers alone (rule 8)
	if f := meta.Failure; f != nil {
		attrs = append(attrs, "stage", f.Stage, "failed_step", f.Step)
		if f.HTTPStatus != 0 {
			attrs = append(attrs, "http_status", f.HTTPStatus)
		}
		if f.WaitedMs != 0 {
			attrs = append(attrs, "waited_ms", f.WaitedMs)
		}
		if f.LimitMs != 0 {
			attrs = append(attrs, "limit_ms", f.LimitMs)
		}
	}
	// names alone: a skill's text is never logged
	if names := tr.loadedSkills(); len(names) > 0 {
		attrs = append(attrs, "skills", strings.Join(names, ","))
	}
	if res.Silent {
		attrs = append(attrs, "silent", true)
	}
	if tr.historyFailed {
		attrs = append(attrs, "history_failed", true)
	}
	if tr.tasksFailed {
		attrs = append(attrs, "tasks_failed", true)
	}
	if tr.inboxFailed {
		attrs = append(attrs, "inbox_failed", true)
	}
	if !stored {
		attrs = append(attrs, "commit_failed", true)
	}
	if resetFailed {
		attrs = append(attrs, "conversation_reset_failed", true)
	}
	r.d.Log.Info("agent_turn", attrs...)
	return meta.Failure
}

// meta is what the turn's row records: its trigger, the mode its last step
// ran in and its tokens over every model call, none when no call reported any.
func (r *Runner) meta(tr *turn) conversation.TurnMeta {
	m := conversation.TurnMeta{Trigger: triggerName(tr.Turn), Mode: tr.mode.Name, Skills: tr.loadedSkills()}
	if tr.metered {
		u := tr.usage
		m.Usage = &u
	}
	return m
}

// loadedSkills are the skills the turn loaded, by input then by tool; nil
// for none
func (tr *turn) loadedSkills() []string {
	if tr.skills == nil {
		return nil
	}
	if names := tr.skills.names(); len(names) > 0 {
		return names
	}
	return nil
}

// triggerNames are the triggers as the conversation history stores them and
// the live feed publishes them; an unspecified one has none.
// triggerName is the turn's trigger by the name the history stores, "" for
// an unspecified one.
func triggerName(t Turn) string {
	if t.Typed {
		return conversation.TriggerDashboard
	}
	return triggerNames[t.Trigger]
}

var triggerNames = map[cadencev2.Trigger]string{
	cadencev2.Trigger_TRIGGER_WAKE:      "wake",
	cadencev2.Trigger_TRIGGER_BUTTON:    "button",
	cadencev2.Trigger_TRIGGER_FOLLOW_UP: "follow_up",
	cadencev2.Trigger_TRIGGER_EVENT:     conversation.TriggerEvent,
	cadencev2.Trigger_TRIGGER_WORD:      "word",
}

// logStep logs one step's counts, the registered tools it called and its
// error codes; names and codes are fixed by the tools and the runner, never
// model text.
func (r *Runner) logStep(tr *turn, start time.Time, rep reply, done []called) {
	var codes, tools []string
	for _, c := range done {
		if c.name != "" && !slices.Contains(tools, c.name) {
			tools = append(tools, c.name)
		}
		if c.err != nil && !slices.Contains(codes, c.err.Code) {
			codes = append(codes, c.err.Code)
		}
	}
	attrs := []any{
		"device_id", tr.DeviceID, "session_id", tr.SessionID, "turn_id", tr.id, "step", tr.steps,
		"tool_calls", len(done), "tools", strings.Join(tools, ","), "tool_errors", strings.Join(codes, ","),
		"prompt_tokens", rep.usage.PromptTokens, "completion_tokens", rep.usage.CompletionTokens,
		"ms", r.d.Now().Sub(start).Milliseconds(),
	}
	if rep.code != "" {
		attrs = append(attrs, "code", rep.code)
	}
	r.d.Log.Info("agent_step", attrs...)
	endStepSpan(tr, rep, len(done))
}

func (r *Runner) publish(tr *turn, typ string, data map[string]any) {
	if r.d.Bus == nil {
		return
	}
	r.d.Bus.Publish(events.Event{
		Type: typ, DeviceID: tr.DeviceID, SessionID: tr.SessionID, TurnID: tr.id, At: r.d.Now(), Data: data,
	})
}

// say sends o and records that the turn said something, and what: a discard
// takes back the current step's speech.
func (r *Runner) say(ctx context.Context, tr *turn, out chan<- Out, o Out) bool {
	select {
	case out <- o:
		tr.said = true
		if o.Discard {
			tr.spoken = tr.spoken[:tr.stepMark]
		} else {
			tr.spoken += o.Speech
		}
		return true
	case <-ctx.Done():
		return false
	}
}

func ctxCode(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return CodeTimeout
	}
	return CodeCanceled
}

// orCtx prefers the turn's own end over a failure it caused.
func orCtx(ctx context.Context, code string) string {
	if ctx.Err() != nil {
		return ctxCode(ctx)
	}
	return code
}

func toolNames(tools []Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}
