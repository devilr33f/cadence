package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
)

const (
	effectShort     = 2500 * time.Millisecond
	effectOther     = 5 * time.Second
	effectSlow      = 12 * time.Second
	effectSlowFloor = 8 * time.Second
	effectCap       = 15 * time.Second
	effectMargin    = 2 * time.Second
	// a learned confirmation time guides a later command for an hour; nothing
	// leaves the process, and a restart returns to the domain's budget
	effectRecent = time.Hour
	// a finished operation stays for its card's usual lifetime
	effectKeep       = 10 * time.Minute
	effectRemembered = 256
)

// HAConfirmationPhase is how far a command whose reply already went out got.
type HAConfirmationPhase string

const (
	HAConfirmationPending HAConfirmationPhase = "pending"
	HAConfirmationDone    HAConfirmationPhase = "done"
	HAConfirmationFailed  HAConfirmationPhase = "failed"
)

// HAConfirmationSnapshot is what a live card shows of a late confirmation.
type HAConfirmationSnapshot struct {
	Phase    HAConfirmationPhase
	State    string
	Expected string
}

// HAConfirmationRecord is a late outcome for the conversation recorder. Args
// are the original call's, Result its final envelope; no second service call
// is made.
type HAConfirmationRecord struct {
	DeviceID, TurnID, CallID, OperationID string
	Args, Result                          json.RawMessage
}

// HAConfirmationOptions are the tracker's collaborators. Short, Cap and
// Margin default to the production bounds; tests may shorten them.
type HAConfirmationOptions struct {
	Now    func() time.Time
	Log    *slog.Logger
	Record func(context.Context, HAConfirmationRecord)
	Short  time.Duration
	Cap    time.Duration
	Margin time.Duration
}

type confirmationLatency struct {
	took time.Duration
	at   time.Time
}

type haOperation struct {
	id      string
	call    agent.Call
	args    controlArgs
	before  ha.Entity
	out     controlOut
	started time.Time
	changed chan struct{}
	at      time.Time
	snap    HAConfirmationSnapshot
}

// HAConfirmations follows accepted commands after their turns replied and
// remembers, in memory only, how quickly each entity confirmed recently.
type HAConfirmations struct {
	home   *ha.Home
	now    func() time.Time
	log    *slog.Logger
	record func(context.Context, HAConfirmationRecord)
	short  time.Duration
	cap    time.Duration
	margin time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	closed bool
	recent map[string]confirmationLatency
	ops    map[string]*haOperation
}

// NewHAConfirmations builds a tracker for home. Close it before home stops.
func NewHAConfirmations(home *ha.Home, o HAConfirmationOptions) *HAConfirmations {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Short <= 0 {
		o.Short = effectShort
	}
	if o.Cap <= 0 {
		o.Cap = effectCap
	}
	if o.Margin <= 0 {
		o.Margin = effectMargin
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &HAConfirmations{
		home: home, now: o.Now, log: o.Log, record: o.Record, short: o.Short, cap: o.Cap, margin: o.Margin,
		ctx: ctx, cancel: cancel, recent: map[string]confirmationLatency{}, ops: map[string]*haOperation{},
	}
}

// effectDomainBudget is a command's budget before that entity has a recent
// confirmation time. Infrared and cloud devices answer their state stream
// seconds after accepting the command; a fast light need not wait that long.
func effectDomainBudget(domain string) time.Duration {
	switch domain {
	case "light", "switch", "input_boolean":
		return effectShort
	case "climate", "water_heater", "cover", "fan", "media_player", "vacuum":
		return effectSlow
	default:
		return effectOther
	}
}

// effectDomainFloor is the least a learned budget can be. In particular, one
// instant climate response must not bring the original 2.5 s false-failure
// window back for that entity's next, slower command.
func effectDomainFloor(domain string) time.Duration {
	switch domain {
	case "climate", "water_heater", "cover", "fan", "media_player", "vacuum":
		return effectSlowFloor
	default:
		return effectDomainBudget(domain)
	}
}

// Budget is the total confirmation budget of entity, from the accepted call.
// Once an entity has a recent observation, a little more than that observation
// replaces the domain default, down to its long floor and up to the cap. This
// lets a usually quick climate entity stop waiting at 8 s after it confirmed
// at 6 s, while an unusually slow light can take longer than 2.5 s.
func (c *HAConfirmations) Budget(entityID, domain string) time.Duration {
	if c == nil {
		return effectDomainBudget(domain)
	}
	budget := min(effectDomainBudget(domain), c.cap)
	c.mu.Lock()
	seen, ok := c.recent[entityID]
	c.mu.Unlock()
	if ok && c.now().Sub(seen.at) <= effectRecent {
		budget = max(min(effectDomainFloor(domain), c.cap), seen.took+c.margin)
	}
	return min(budget, c.cap)
}

// note learns a real confirmation, never an already-satisfied no-op.
func (c *HAConfirmations) note(entityID string, took time.Duration) {
	if took < 0 {
		took = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if _, exists := c.recent[entityID]; !exists && len(c.recent) >= effectRemembered {
		var oldest string
		var at time.Time
		for id, seen := range c.recent {
			if oldest == "" || seen.at.Before(at) {
				oldest, at = id, seen.at
			}
		}
		delete(c.recent, oldest)
	}
	c.recent[entityID] = confirmationLatency{took: took, at: c.now()}
}

// Watch returns the operation's current state and a channel closed by its
// next state. A completed operation has no update pending: its new channel is
// open until another caller uses the same id (which callers never do).
func (c *HAConfirmations) Watch(id string) (HAConfirmationSnapshot, <-chan struct{}, bool) {
	if c == nil {
		return HAConfirmationSnapshot{}, nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	op := c.ops[id]
	if op == nil {
		return HAConfirmationSnapshot{}, nil, false
	}
	return op.snap, op.changed, true
}

// Close ends every outstanding wait and waits for a late-history write already
// running. It does not record a shutdown as a device failure.
func (c *HAConfirmations) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	c.cancel()
	c.wg.Wait()
}

// confirm waits only the short foreground window. A fixed action with budget
// left returns pending and follows the rest detached from the turn; a fast
// domain already at its whole budget returns the real no_effect.
func (c *HAConfirmations) confirm(ctx context.Context, call agent.Call, a controlArgs, before ha.Entity, out controlOut) (controlOut, *agent.ToolError) {
	started := c.now()
	after, ok := c.home.Wait(ctx, before.ID, shown(a, before), c.short)
	if ok {
		took := c.now().Sub(started)
		c.note(before.ID, took)
		out.Entity.State = ha.ModelState(after)
		return out, nil
	}
	// a service the model picked itself may have no observable state target;
	// its accepted call is not a failure merely because the cache stayed put
	if a.action == "call_service" {
		return out, nil
	}
	remaining := c.Budget(before.ID, before.Domain) - c.now().Sub(started)
	if remaining <= 0 {
		return controlOut{}, noEffect(after)
	}
	id := call.TurnID + ":" + call.CallID
	if call.TurnID == "" && call.CallID == "" {
		id = "ha-" + rand.Text()
	}
	out.Pending = true
	out.State = ha.ModelState(after)
	out.Expected = expectedState(a, before)
	out.Entity.State = out.State
	out.Confirmation = id
	op := &haOperation{
		id: id, call: call, args: a, before: before, out: out, started: started, changed: make(chan struct{}), at: c.now(),
		snap: HAConfirmationSnapshot{Phase: HAConfirmationPending, State: out.State, Expected: out.Expected},
	}
	if !c.launch(op, remaining) {
		return controlOut{}, noEffect(after)
	}
	return out, nil
}

// launch registers op before its follower starts, so a very fast late event
// cannot finish before a card can find the pending operation.
func (c *HAConfirmations) launch(op *haOperation, remaining time.Duration) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.pruneLocked(c.now())
	c.ops[op.id] = op
	c.wg.Add(1)
	c.mu.Unlock()
	go c.follow(op, remaining)
	return true
}

// pruneLocked bounds finished history without a sweeper goroutine. Pending
// operations are bounded by their confirmation budgets instead.
func (c *HAConfirmations) pruneLocked(now time.Time) {
	for id, op := range c.ops {
		if op.snap.Phase != HAConfirmationPending && now.Sub(op.at) > effectKeep {
			delete(c.ops, id)
		}
	}
	if len(c.ops) < effectRemembered {
		return
	}
	var oldest string
	var at time.Time
	for id, op := range c.ops {
		if op.snap.Phase != HAConfirmationPending && (oldest == "" || op.at.Before(at)) {
			oldest, at = id, op.at
		}
	}
	if oldest != "" {
		delete(c.ops, oldest)
	}
}

// follow completes the rest of op's budget. The card changes before the
// history write, which has its own short bound and cannot delay the screen.
func (c *HAConfirmations) follow(op *haOperation, remaining time.Duration) {
	defer c.wg.Done()
	ctx, cancel := context.WithTimeout(c.ctx, remaining)
	after, ok := c.home.Wait(ctx, op.before.ID, shown(op.args, op.before), remaining)
	cancel()
	if c.ctx.Err() != nil {
		c.finish(op, HAConfirmationFailed, ha.ModelState(after))
		return
	}
	took := c.now().Sub(op.started)
	var result json.RawMessage
	var code string
	if ok {
		c.note(op.before.ID, took)
		op.out.Pending = false
		op.out.Late = true
		op.out.State = ha.ModelState(after)
		op.out.Entity.State = op.out.State
		result, _ = json.Marshal(struct {
			OK   bool       `json:"ok"`
			Data controlOut `json:"data"`
		}{OK: true, Data: op.out})
		code = "confirmed"
	} else {
		result, _ = json.Marshal(struct {
			OK    bool             `json:"ok"`
			Error *agent.ToolError `json:"error"`
		}{Error: noEffect(after)})
		code = "no_effect"
	}
	phase := HAConfirmationDone
	if !ok {
		phase = HAConfirmationFailed
	}
	c.finish(op, phase, ha.ModelState(after))
	c.log.Info("ha_confirmation", "device_id", op.call.DeviceID, "turn_id", op.call.TurnID, "call_id", op.call.CallID,
		"code", code, "ms", took.Milliseconds())
	if c.record == nil {
		return
	}
	// a recorder is app code: its panic must not take down the confirmation
	// follower, and its text never reaches this log
	func() {
		defer func() {
			if recover() != nil {
				c.log.Error("ha_confirmation_record_failed", "device_id", op.call.DeviceID, "turn_id", op.call.TurnID,
					"code", "panic")
			}
		}()
		rctx, stop := context.WithTimeout(c.ctx, usageTimeout)
		defer stop()
		c.record(rctx, HAConfirmationRecord{
			DeviceID: op.call.DeviceID, TurnID: op.call.TurnID, CallID: op.call.CallID, OperationID: op.id,
			Args: op.call.Args, Result: result,
		})
	}()
}

// finish publishes exactly one terminal state and wakes leased cards.
func (c *HAConfirmations) finish(op *haOperation, phase HAConfirmationPhase, state string) {
	c.mu.Lock()
	op.snap.Phase = phase
	op.snap.State = state
	op.at = c.now()
	close(op.changed)
	op.changed = make(chan struct{})
	c.mu.Unlock()
}

// expectedState is the fixed action's observable target, in the tool's own
// enumerated values or numeric fields, never Home Assistant's text.
func expectedState(a controlArgs, before ha.Entity) string {
	switch a.action {
	case "turn_on":
		if before.Domain == "cover" {
			return "open"
		}
		return "on"
	case "turn_off":
		if before.Domain == "cover" {
			return "closed"
		}
		return "off"
	case "toggle":
		if readsOn(before) {
			if before.Domain == "cover" {
				return "closed"
			}
			return "off"
		}
		if before.Domain == "cover" {
			return "open"
		}
		return "on"
	case "set_hvac_mode":
		return a.mode
	case "set_brightness":
		return figure(float64(a.brightness)) + "%"
	case "set_color_temp":
		return figure(float64(a.kelvin)) + " K"
	case "set_temperature":
		return figure(a.temperature)
	}
	return ""
}

// noEffect is a real no-change after the entity's whole budget, never merely
// the end of the foreground window.
func noEffect(after ha.Entity) *agent.ToolError {
	msg := "The device took the command but did not change."
	if s := ha.ModelState(after); s != "" {
		msg = "The device took the command but still reads " + strconv.Quote(s) + "."
	}
	return &agent.ToolError{
		Code: "no_effect", Message: msg,
		Hint: "Tell the user it did not work. Never say the change happened.",
	}
}
