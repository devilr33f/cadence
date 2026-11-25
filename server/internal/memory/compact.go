package memory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"

	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

const (
	// RetryAfter holds the idle trigger back from messages whose compaction
	// failed: without it every sweep would ask the model again.
	RetryAfter = 30 * time.Minute

	// defaultCompactAfter is in units (CompactorDeps.Unit)
	defaultCompactAfter = 10
	keepCompactions     = 50
	// maxWindow caps a model's window, in tokens, for the size trigger and
	// the input's budget: a summary of a million tokens' worth of turns
	// would be neither cheap nor good
	maxWindow = 131_072
	// maxJobs bounds the jobs that run at once, across devices
	maxJobs = 3
	// settle keeps a job off messages younger than this. Ids are drawn
	// before commit, so a slower commit can still land below the newest id
	// read; the agent bounds a commit at 5 s, so after settle none can.
	settle = 10 * time.Second
	// segmentLimit bounds the messages a job reads; the oldest past it are
	// left out, as the input's budget would leave them
	segmentLimit   = 2000
	jobTimeout     = 3 * time.Minute
	attemptTimeout = 60 * time.Second
	hookTimeout    = 3 * time.Minute
	sizeTimeout    = 5 * time.Second
)

const (
	triggerIdle   = "idle"
	triggerSize   = "size"
	triggerReset  = "reset"
	triggerManual = "manual"
)

// Compacted is what a stored compaction hands the hook.
type Compacted struct {
	Compaction Compaction
	// Segment is the messages the summary took in, oldest first, as the
	// model read them (redacted).
	Segment []conversation.Stored
	// limits is the call that wrote the summary; learning uses it too.
	limits callLimits
	// Untrusted holds the ids of the segment's messages Redact withheld.
	Untrusted map[int64]bool
	// LLM wrote the summary, on Compaction.Model.
	LLM providers.LLM
}

type CompactorDeps struct {
	Store    *Store
	Messages interface {
		After(ctx context.Context, deviceID string, afterID int64, before time.Time, limit int) ([]conversation.Stored, error)
		Through(ctx context.Context, deviceID string, afterID, uptoID int64, limit int) ([]conversation.Stored, error)
	}
	Settings func(ctx context.Context, deviceID string) (settings.Values, error)
	LLMs     interface {
		LLM(ctx context.Context, name string) (providers.LLM, error)
	}
	// Window is a model's context window in tokens.
	Window func(model string) int
	// Redact returns the messages, one for one, with what no model may read
	// again replaced (untrusted tool results); nil keeps them all.
	Redact func([]providers.Message) []providers.Message
	// Hook runs after each stored compaction, in the device's job; nil does nothing.
	Hook func(ctx context.Context, c Compacted)
	// Mode reads the compaction mode. Nil keeps the maintenance model.
	Mode ModeFunc
	Bus  *events.Bus
	Log  *slog.Logger
	Now  func() time.Time
	// Unit is one step of the compactAfter setting; zero is a minute. Only
	// the dev stack shortens it.
	Unit time.Duration
	// Every is the idle trigger's interval; zero is half a Unit (30 s).
	Every time.Duration
}

// Compactor folds each device's conversation into a rolling summary
// (M5a-D4), one job per device at a time: a trigger while the device's job
// runs is dropped, since that job takes in every settled message committed
// before it; what it leaves stays raw for the next trigger.
type Compactor struct {
	d      CompactorDeps
	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	slots  chan struct{} // maxJobs

	mu      sync.Mutex
	closed  bool
	running map[string]bool
	failed  map[string]failure
	due     map[string]due
}

// failure is the newest message of an idle compaction that failed, and when.
type failure struct {
	newest int64
	at     time.Time
}

// due is when the idle trigger compacts a device's conversation up to its
// newest message: compactAfter is read once per newest message, so a sweep
// does not read every waiting device's settings.
type due struct {
	newest int64
	at     time.Time
}

func NewCompactor(d CompactorDeps) *Compactor {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Unit <= 0 {
		d.Unit = time.Minute
	}
	if d.Every <= 0 {
		d.Every = d.Unit / 2
	}
	base, cancel := context.WithCancel(context.Background())
	return &Compactor{
		d: d, base: base, cancel: cancel, slots: make(chan struct{}, maxJobs),
		running: map[string]bool{}, failed: map[string]failure{}, due: map[string]due{},
	}
}

// Run sweeps at once and then every interval until ctx ends; stop ends the
// loop and the jobs, and waits for both.
func (c *Compactor) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.sweep(ctx)
		t := time.NewTicker(c.d.Every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.sweep(ctx)
			}
		}
	}()
	return func() { cancel(); <-done; c.Close() }
}

// Close cancels the running jobs, waits for them and starts no more.
func (c *Compactor) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.cancel()
	c.wg.Wait()
}

// Start is the message id the device's current context starts after.
func (c *Compactor) Start(ctx context.Context, deviceID string) (int64, error) {
	return c.d.Store.ConversationStart(ctx, deviceID)
}

// Latest is the device's latest compaction, ErrNotFound when it has none.
func (c *Compactor) Latest(ctx context.Context, deviceID string) (Compaction, error) {
	return c.d.Store.LatestCompaction(ctx, deviceID)
}

// Committed is the size trigger, for a turn just stored: when the messages
// since the latest compaction outgrow 70 % of window (the turn's model's,
// in tokens, at most maxWindow), the device is compacted in the background.
// The turn itself is younger than settle, so it stays raw until the next job.
func (c *Compactor) Committed(deviceID string, window int) {
	c.start(deviceID, func(ctx context.Context) {
		sctx, cancel := context.WithTimeout(ctx, sizeTimeout)
		chars, err := c.d.Store.UncompactedChars(sctx, deviceID)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				c.d.Log.Warn("compaction_size_failed", "device_id", deviceID)
			}
			return
		}
		if oversize(chars, min(window, maxWindow)) {
			_ = c.job(ctx, deviceID, triggerSize)
		}
	})
}

// CompactClosed compacts exactly the segment a reset closed, through its
// boundary and without the idle settle wait. Its summary stays behind the
// boundary for learning; prompt assembly never reads it into the new context.
func (c *Compactor) CompactClosed(deviceID string, uptoID int64) bool {
	if uptoID <= 0 {
		return true
	}
	return c.start(deviceID, func(ctx context.Context) {
		_ = c.job(ctx, deviceID, triggerReset, compactOptions{closed: true, uptoID: uptoID})
	})
}

// RequestCompaction is the dashboard's compact-now trigger: unlike the idle
// trigger it includes the newest committed messages. false when a job runs.
func (c *Compactor) RequestCompaction(deviceID string) bool {
	return c.start(deviceID, func(ctx context.Context) {
		_ = c.job(ctx, deviceID, triggerManual, compactOptions{immediate: true})
	})
}

// oversize reports whether chars, at charsPerToken, are over 70 % of window tokens.
func oversize(chars int64, window int) bool {
	return chars/charsPerToken*10 > int64(window)*7
}

// sweep is the idle trigger: it starts a job for each device whose newest
// message, newer than its latest compaction, is compactAfter old and settled,
// so that the job takes it in: a short Unit can put compactAfter under the
// settle time. It returns the devices whose job started.
func (c *Compactor) sweep(ctx context.Context) []string {
	cands, err := c.d.Store.Candidates(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.d.Log.Warn("compaction_sweep_failed")
		}
		return nil
	}
	now := c.d.Now()
	var started []string
	for _, cand := range cands {
		if ctx.Err() != nil {
			break
		}
		if idle := now.Sub(cand.NewestAt); idle < settings.MinCompactAfter*c.d.Unit || idle <= settle || c.holding(cand, now) {
			continue
		}
		at, ok := c.dueAt(cand)
		if !ok {
			v, err := c.d.Settings(ctx, cand.DeviceID)
			if err != nil {
				continue
			}
			at = cand.NewestAt.Add(c.compactAfter(v))
			c.mu.Lock()
			c.due[cand.DeviceID] = due{newest: cand.NewestID, at: at}
			c.mu.Unlock()
		}
		if now.Before(at) {
			continue
		}
		if c.start(cand.DeviceID, func(ctx context.Context) {
			if c.job(ctx, cand.DeviceID, triggerIdle) != nil && ctx.Err() == nil {
				c.mu.Lock()
				c.failed[cand.DeviceID] = failure{newest: cand.NewestID, at: c.d.Now()}
				c.mu.Unlock()
			}
		}) {
			started = append(started, cand.DeviceID)
			c.mu.Lock()
			delete(c.due, cand.DeviceID)
			c.mu.Unlock()
		}
	}
	return started
}

// dueAt is when the candidate's conversation is due, if known.
func (c *Compactor) dueAt(cand Candidate) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.due[cand.DeviceID]
	return d.at, ok && d.newest == cand.NewestID
}

func (c *Compactor) compactAfter(v settings.Values) time.Duration {
	n := v.CompactAfter
	if n <= 0 {
		n = defaultCompactAfter
	}
	return time.Duration(n) * c.d.Unit
}

// holding reports whether the candidate's messages failed to compact within RetryAfter.
func (c *Compactor) holding(cand Candidate, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.failed[cand.DeviceID]
	return ok && f.newest == cand.NewestID && now.Sub(f.at) < RetryAfter
}

// start runs fn as the device's job unless one runs or the compactor closed.
func (c *Compactor) start(deviceID string, fn func(ctx context.Context)) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.running[deviceID] {
		return false
	}
	c.running[deviceID] = true
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			c.mu.Lock()
			delete(c.running, deviceID)
			c.mu.Unlock()
		}()
		defer func() {
			// the value can carry text, so it is dropped
			if recover() != nil {
				c.d.Log.Error("compaction_failed", "device_id", deviceID, "code", codeInternal)
			}
		}()
		fn(c.base)
	}()
	return true
}

// compactOptions bound a manual job. closed is a reset's old segment: it keeps
// the previous summary as its input, but has a hard upper id. immediate is
// compact-now; the idle and size triggers keep their settle window.
type compactOptions struct {
	closed, immediate bool
	uptoID            int64
}

// job compacts the device, logs and publishes how it went and runs the
// hook. It returns the failure; nil also when nothing was new.
func (c *Compactor) job(ctx context.Context, deviceID, trigger string, options ...compactOptions) error {
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return jobError(codeCanceled)
	}
	start := c.d.Now()
	// a compaction and the learning it hooks are a trace of their own
	ctx, span := tracing.Root(ctx, "memory", "memory.compaction",
		attribute.String(tracing.AttrDeviceID, deviceID), attribute.String("cadence.trigger", trigger))
	defer span.End()
	jctx, cancel := context.WithTimeout(ctx, jobTimeout)
	done, err := c.compact(jctx, deviceID, options...)
	cancel()
	if err != nil {
		tracing.Fail(span, code(err))
		c.d.Log.Warn("compaction_failed", "device_id", deviceID, "code", code(err))
		return err
	}
	if done == nil {
		return nil
	}
	c.mu.Lock()
	delete(c.failed, deviceID)
	c.mu.Unlock()
	ms := c.d.Now().Sub(start).Milliseconds()
	chars := utf8.RuneCountInString(done.Compaction.Summary)
	span.SetAttributes(attribute.Int("cadence.messages", len(done.Segment)), attribute.Int("cadence.chars", chars))
	c.d.Log.Info("compacted", "device_id", deviceID, "trigger", trigger, "messages", len(done.Segment), "chars", chars, "ms", ms)
	if c.d.Bus != nil {
		// chars is the summary's length: the event carries no text
		c.d.Bus.Publish(events.Event{
			Type: "compaction", DeviceID: deviceID, At: c.d.Now(),
			Data: map[string]any{"messages": len(done.Segment), "chars": chars, "ms": ms},
		})
	}
	if c.d.Hook != nil {
		hctx, cancel := context.WithTimeout(ctx, hookTimeout)
		defer cancel()
		c.d.Hook(hctx, *done)
	}
	return nil
}

// compact summarizes the previous summary and the messages since into a new
// compaction; nil without new messages. A regular job at a reset boundary
// starts with neither the closed segment's summary nor its raw turns.
func (c *Compactor) compact(ctx context.Context, deviceID string, options ...compactOptions) (*Compacted, error) {
	var option compactOptions
	if len(options) > 0 {
		option = options[0]
	}
	v, err := c.d.Settings(ctx, deviceID)
	if err != nil {
		return nil, failed(ctx, codeInternal)
	}
	if v.LLM == "" {
		return nil, jobError(codeNotConfigured)
	}
	llm, err := c.d.LLMs.LLM(ctx, v.LLM)
	if err != nil {
		if errors.Is(err, providers.ErrNotFound) || errors.Is(err, providers.ErrWrongKind) || errors.Is(err, providers.ErrUnknownModule) {
			return nil, jobError(codeNotConfigured)
		}
		return nil, failed(ctx, providers.CodeLLMFailed)
	}
	var prev *Compaction
	var after int64
	switch latest, err := c.d.Store.LatestCompaction(ctx, deviceID); {
	case err == nil:
		prev, after = &latest, latest.UptoMessageID
	case !errors.Is(err, ErrNotFound):
		return nil, failed(ctx, codeStore)
	}
	if !option.closed {
		start, err := c.d.Store.ConversationStart(ctx, deviceID)
		if err != nil {
			return nil, failed(ctx, codeStore)
		}
		if prev == nil || start > 0 && prev.UptoMessageID <= start {
			prev, after = nil, start
		}
	}
	var seg []conversation.Stored
	switch {
	case option.closed || option.immediate:
		upto := option.uptoID
		if upto == 0 {
			upto = math.MaxInt64
		}
		seg, err = c.d.Messages.Through(ctx, deviceID, after, upto, segmentLimit)
	default:
		seg, err = c.d.Messages.After(ctx, deviceID, after, c.d.Now().Add(-settle), segmentLimit)
	}
	if err != nil {
		return nil, failed(ctx, codeStore)
	}
	if len(seg) == 0 {
		return nil, nil
	}
	lim, err := resolveCall(ctx, c.d.Mode, v)
	if err != nil {
		return nil, err
	}
	untrusted, err := c.redact(seg)
	if err != nil {
		return nil, err
	}
	out, err := summarize(ctx, llm, lim, compactInput(prev, seg, min(c.d.Window(lim.model), maxWindow)))
	if err != nil {
		return nil, err
	}
	threads, err := json.Marshal(out.Threads)
	if err != nil {
		return nil, jobError(codeInternal)
	}
	row, err := c.d.Store.AddCompaction(ctx, NewCompaction{
		DeviceID: deviceID, UptoMessageID: seg[len(seg)-1].ID, Summary: out.Summary, OpenThreads: threads,
		TaskRefs: []byte("[]"), Model: lim.model, // task refs stay empty until tasks exist (M5b)
	})
	if err != nil {
		return nil, failed(ctx, codeStore)
	}
	if _, err := c.d.Store.PruneCompactions(ctx, deviceID, keepCompactions); err != nil {
		c.d.Log.Warn("compaction_prune_failed", "device_id", deviceID)
	}
	return &Compacted{Compaction: row, Segment: seg, Untrusted: untrusted, LLM: llm, limits: lim}, nil
}

// redact applies Redact to the segment in place and returns the ids of the
// messages it withheld.
func (c *Compactor) redact(seg []conversation.Stored) (map[int64]bool, error) {
	if c.d.Redact == nil {
		return nil, nil
	}
	msgs := make([]providers.Message, len(seg))
	for i, m := range seg {
		msgs[i] = m.Message
	}
	out := c.d.Redact(msgs)
	if len(out) != len(seg) {
		return nil, jobError(codeInternal)
	}
	withheld := map[int64]bool{}
	for i := range seg {
		if out[i].Content != seg[i].Content {
			withheld[seg[i].ID] = true
		}
		seg[i].Message = out[i]
	}
	return withheld, nil
}
