package alarms

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	// how long a ring sounds when nothing stops it, from its start, by default (v1); the config sets them
	AlarmLimit = 600 * time.Second
	TimerLimit = 60 * time.Second

	// a ring audiod cannot sound is asked for again this often, lights only meanwhile (v1, S0 §7.5)
	retryEvery = time.Second
	// the most rings that wait behind the one that sounds; past it a ring is missed
	maxWaiting = maxRings
	// the loop never wakes more often than this, whatever the book says
	minWake = 10 * time.Millisecond

	// what audiod says of a tone that is not sounding (audiod.ToneStopped, audiod.ToneInvalid)
	toneStopped = "stopped"
	toneInvalid = "invalid"
)

// Speaker sounds the rings: the board's audiod, which loops a tone from StartTone to StopTone, answers each start
// with a ToneState, and sets an alarm's ramp on its output without making it the level in force.
type Speaker interface {
	StartTone(id string, kind audiodv1.ToneKind) error
	StopTone(id string) error
	SetTransientLevel(level int) error
}

// Volume is the board's output level: an alarm's ramp raises the hardware above the level in force through the path
// it follows, and a stop puts the level in force back.
type Volume interface {
	Current() (level int, known bool)
	FollowTransient(apply func(level int) error)
	Transient(level int) (raised bool, err error)
	// sets the hardware at or below the level in force, for a quiet ramp: lowering is always allowed
	Quiet(level int) (set bool, err error)
	Reassert()
	// the hardware lost the raise: Reassert finds nothing above the level in force
	DropTransient()
}

// StopReason says what stopped a ring.
type StopReason string

const (
	StopButton   StopReason = "button"
	StopVolume   StopReason = "volume"
	StopWake     StopReason = "wake"
	StopWord     StopReason = "word"
	StopTimeout  StopReason = "timeout"
	StopShutdown StopReason = "shutdown"
	// a snooze put it off
	StopSnooze StopReason = "snooze"
	// a stop_ring command: a screen, the dashboard or Home Assistant
	StopRemote StopReason = "remote"
)

func (r StopReason) valid() bool {
	switch r {
	case StopButton, StopVolume, StopWake, StopWord, StopTimeout, StopShutdown, StopSnooze, StopRemote:
		return true
	}
	return false
}

// RingCounts hears each ring that begins and each that stops, by kind (alarm or timer), for the metrics; it must
// never block.
type RingCounts interface {
	RingBegan(kind string)
	RingStopped(kind string)
}

// Hooks are what core does about a ring beyond the board; a nil one does nothing.
type Hooks struct {
	// ends the session that runs, cutting its reply and the announcement: a ring begins while none rang
	EndSession func()
	// shows a ring on the lights, or stops showing it
	Ringing func(on bool)
}

type RingerOptions struct {
	Book   *Book
	Volume Volume
	// how the sessions the commands name began; nil knows none
	Sessions Sessions
	// nil discards
	Log *slog.Logger
	// defaults to time.Now
	Now func() time.Time
	// how long a timer rings when nothing stops it, over the config's; 0 is the config's. only the sim's e2e shortens
	// it (CADENCE_SIM_TIMER_SECONDS)
	TimerLimit time.Duration
	// nil: no ring is counted
	Counts RingCounts
}

// Ringer rings what the book hands out, one ring at a time and the rest in turn behind it: it sounds each on the
// speaker, ramps an alarm's level up from the level in force, and stops it on a button, a wake, a stop word, its time
// limit or shutdown. While the speaker cannot sound a ring, the lights ring alone and the speaker is asked again about
// every second. It is the input's Ringer. Safe for concurrent use; Run is core's loop.
type Ringer struct {
	book *Book
	vol  Volume
	log  *slog.Logger
	now  func() time.Time
	cap  *provider
	kick chan struct{}
	// how long a timer rings when nothing stops it, over the config's; 0 for the config's
	timerLimit time.Duration
	// nil for none
	counts RingCounts

	// audiod's answers, taken by the loop: ToneState runs on the supervisor's goroutine and never waits for mu
	toneMu sync.Mutex
	tones  []*audiodv1.ToneState
	// whether cur is set, mirrored under mu, so Ringing never waits while mu is held over a send to audiod
	ringing atomic.Bool

	mu      sync.Mutex
	speaker Speaker
	// where the rings that name a sound stream it from, and the language of a ring's bundled prompt; nil for none
	sounds  Sounds
	lang    func() string
	hooks   Hooks
	cur     *ring
	waiting []Ring
	// counts rings, so each gets a tone id of its own
	n      int
	closed bool
}

// ring is the one that sounds, or rings on the lights while the speaker cannot sound it
type ring struct {
	Ring
	// the tone's id on the speaker
	key   string
	began time.Time
	// a StartTone went out, so a stop takes the tone back
	asked bool
	// the speaker said it sounds
	sounding bool
	// when to ask the speaker again while it does not sound; never after it called the ring invalid
	retryAt time.Time
	invalid bool
	// why it is unheard, as last logged
	unheard string
	// the ramp: from the level in force when the ring was first heard (rampRead), or quiet from where the first ask
	// set it, to the alarm's level; ramped is the level the speaker was last set to since it was heard, raised whether
	// any level went out at all
	ramp     bool
	rampRead bool
	rampFrom int
	ramped   int
	raised   bool
	// taken from the alarm's own options, else the config, when the ring began
	rampFor time.Duration
	quiet   bool
	limit   time.Duration
}

// Open is the ringer a board builds: over the book kept under its data dir, by the system's clock, with the board's
// volume and the sessions it records. The book holds its alarms until the capability's config gives it a zone.
func Open(dataDir string, vol Volume, sessions Sessions, log *slog.Logger, opts ...func(*RingerOptions)) *Ringer {
	book := openBook(StorePath(dataDir), SystemClock{}, log)
	o := RingerOptions{Book: book, Volume: vol, Sessions: sessions, Log: log}
	for _, opt := range opts {
		opt(&o)
	}
	return NewRinger(o)
}

func NewRinger(o RingerOptions) *Ringer {
	r := &Ringer{
		book: o.Book, vol: o.Volume, log: o.Log, now: o.Now, kick: make(chan struct{}, 1), timerLimit: o.TimerLimit,
		counts: o.Counts,
	}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	if r.now == nil {
		r.now = time.Now
	}
	r.cap = newProvider(o.Book, o.Sessions, r.log)
	r.cap.ringer = r
	return r
}

// UseSpeaker sounds the rings on s from now on, and ramps an alarm's level through it: the board's audiod, once it
// runs. Until then rings are lights only.
func (r *Ringer) UseSpeaker(s Speaker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.speaker = s
	r.vol.FollowTransient(s.SetTransientLevel)
}

// ToneState takes audiod's answer to a StartTone, or the end of a ring; it never blocks.
func (r *Ringer) ToneState(st *audiodv1.ToneState) {
	r.toneMu.Lock()
	r.tones = append(r.tones, st)
	r.toneMu.Unlock()
	r.wake()
}

// Ringing is whether a ring sounds, or rings on the lights; it never waits.
func (r *Ringer) Ringing() bool { return r.ringing.Load() }

// setCur makes c the ring that sounds, nil for none. callers hold mu
func (r *Ringer) setCur(c *ring) {
	r.cur = c
	r.ringing.Store(c != nil)
}

// StopRing stops the ring for a press of the action button or either volume button, which then does nothing else.
func (r *Ringer) StopRing(by input.Kind) {
	reason := StopButton
	if by == input.VolumeUp || by == input.VolumeDown {
		reason = StopVolume
	}
	r.Stop(reason)
}

// Stop dismisses the ring that sounds, if any; the next one waiting starts at once. It reports whether a ring rings
// now.
func (r *Ringer) Stop(reason StopReason) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur != nil && !r.closed {
		r.stop(reason, r.now())
		r.wake()
	}
	return r.cur != nil
}

// Run rings until ctx ends, waking at the book's next due second (at most a minute away), on each change of it, on
// audiod's answers and for the ring's own times; it then stops the ring that sounds and misses the waiting ones.
func (r *Ringer) Run(ctx context.Context, hooks Hooks) {
	changes := r.book.Changes()
	r.begin(hooks)
	var wg sync.WaitGroup
	wg.Go(func() { r.cap.follow(ctx) })
	defer wg.Wait()
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.shutdown()
			return
		case <-t.C:
		case <-changes:
		case <-r.kick:
		}
		t.Reset(r.step(r.now()))
	}
}

// Capability is the alarms capability over the ringer's book.
func (r *Ringer) Capability() capability.Provider { return r.cap }

// Book is what the ringer rings.
func (r *Ringer) Book() *Book { return r.book }

func (r *Ringer) begin(hooks Hooks) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = hooks
}

func (r *Ringer) wake() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// step does what is due at now and returns how long until it next has something to do
func (r *Ringer) step(now time.Time) time.Duration {
	answers := r.takeTones()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return time.Hour
	}
	for _, st := range answers {
		r.answer(st)
	}
	for _, due := range r.book.TakeDue(now) {
		r.enqueue(due, now)
	}
	if c := r.cur; c != nil {
		switch {
		case now.Sub(c.began) >= c.limit:
			r.timeout(c, now)
		case !c.sounding && !c.invalid && !now.Before(c.retryAt):
			r.ask(c, now)
		case c.sounding:
			r.rampTo(c, now)
		}
	}
	return r.next(now)
}

func (r *Ringer) takeTones() []*audiodv1.ToneState {
	r.toneMu.Lock()
	defer r.toneMu.Unlock()
	out := r.tones
	r.tones = nil
	return out
}

// options are the ring's ramp and limit: the alarm's own, else the config's
func (r *Ringer) options(c *ring) {
	s := r.book.Settings()
	o := c.Options
	switch c.Kind {
	case KindAlarm:
		c.limit = s.AlarmLimit
		if o.LimitSeconds > 0 {
			c.limit = time.Duration(o.LimitSeconds) * time.Second
		}
	case KindReminder:
		c.limit = s.ReminderLimit
	default:
		c.limit = s.TimerLimit
		if r.timerLimit > 0 {
			c.limit = r.timerLimit
		}
	}
	c.rampFor = s.Ramp
	if o.HasRamp {
		c.rampFor = time.Duration(o.RampSeconds) * time.Second
	}
	from := s.RampFrom
	if o.RampFrom != "" {
		from = o.RampFrom
	}
	c.quiet = c.ramps() && from == RampQuiet
}

// ramps is a ring that sets its own level: an alarm, or a reminder set with one
func (c *ring) ramps() bool { return c.Kind == KindAlarm || c.Kind == KindReminder && c.Level > 0 }

// timeout stops a ring nobody stopped: an alarm rings again SnoozeMinutes later while it has auto-snoozes left.
// callers hold mu
func (r *Ringer) timeout(c *ring, now time.Time) {
	s := r.book.Settings()
	again := c.Kind == KindAlarm && c.AutoSnoozes < s.AutoSnooze
	r.log.Info("ring_timeout", "kind", string(c.Kind), "id", c.ID, "auto_snooze", c.AutoSnoozes)
	ringing := c.Ring
	r.stop(StopTimeout, now)
	if again {
		ringing.AutoSnoozes++
		_, _ = r.book.SnoozeRing(ringing, s.SnoozeMinutes)
	}
}

// enqueue starts a ring, or queues it behind the one that sounds. one that is already sounding or waiting is ignored,
// and an alarm of the occurrence an alarm sounds or waits for is the same wake-up: it merges into it. callers hold mu
func (r *Ringer) enqueue(due Ring, now time.Time) {
	if r.cur == nil {
		r.start(due, now, true)
		return
	}
	same := func(o Ring) bool {
		return o.Kind == due.Kind && (o.ID == due.ID || due.Kind == KindAlarm && o.At.Equal(due.At))
	}
	if same(r.cur.Ring) {
		return
	}
	for _, w := range r.waiting {
		if same(w) {
			return
		}
	}
	if len(r.waiting) >= maxWaiting {
		r.missed(due)
		return
	}
	r.waiting = append(r.waiting, due)
}

// start rings due: a ring that begins while none rang ends the session that runs. callers hold mu
func (r *Ringer) start(due Ring, now time.Time, first bool) {
	r.n++
	c := &ring{Ring: due, key: fmt.Sprintf("ring-%d", r.n), began: now}
	r.options(c)
	r.setCur(c)
	r.log.Info("ring_start", "kind", string(due.Kind), "id", due.ID)
	if r.counts != nil {
		r.counts.RingBegan(string(due.Kind))
	}
	if first {
		if r.hooks.EndSession != nil {
			r.hooks.EndSession()
		}
		if r.hooks.Ringing != nil {
			r.hooks.Ringing(true)
		}
	}
	r.cap.setRinging(&due)
	r.ask(c, now)
}

// ask has the speaker sound the ring, and asks again a second later unless it answers that it sounds. callers hold mu
func (r *Ringer) ask(c *ring, now time.Time) {
	c.retryAt = now.Add(retryEvery)
	if r.speaker == nil {
		r.unheard(c, "unavailable")
		return
	}
	var err error
	start := r.toneStart(c)
	if rs, ok := r.speaker.(RingSpeaker); ok && start != nil {
		err = rs.StartRing(start)
	} else {
		err = r.speaker.StartTone(c.key, toneKind(c.Kind))
	}
	if err != nil {
		r.unheard(c, "unavailable")
		return
	}
	c.asked = true
	r.quietStart(c, now)
}

// quietStart sets a quiet ramp's level right behind its tone's start, as audiod sets a ramp's level only while a ring
// sounds: QuietLevel, or the level in force or the alarm's own when either is lower. a ring asked for again (its
// level lost to a gate) is set again where its ramp stands. callers hold mu
func (r *Ringer) quietStart(c *ring, now time.Time) {
	if !c.quiet || c.ramped > 0 {
		return
	}
	if !c.rampRead {
		level, known := r.vol.Current()
		if !known {
			return
		}
		c.rampRead = true
		c.rampFrom = min(QuietLevel, level, c.Level)
		c.ramp = c.Level > c.rampFrom
	}
	r.setLevel(c, RampLevel(c.rampFrom, c.Level, now.Sub(c.began), c.rampFor))
}

// answer takes audiod's word on the ring that sounds: an answer for any other is stale. callers hold mu
func (r *Ringer) answer(st *audiodv1.ToneState) {
	c := r.cur
	if c == nil || st.GetId() != c.key {
		return
	}
	if st.GetSounding() {
		if st.GetFallback() {
			// the ring left its sound (the server or the network is down) for its tone: it still rings
			r.log.Warn("ring_fallback", "kind", string(c.Kind), "id", c.ID, "code", codeToken(st.GetCode()))
		}
		if !c.sounding {
			c.sounding, c.unheard = true, ""
			r.heard(c)
		}
		return
	}
	switch code := st.GetCode(); code {
	case toneStopped:
		// only a stop of ours says so, and a stopped ring is no longer this one
	case toneInvalid:
		c.sounding, c.invalid = false, true
		r.unheard(c, code)
	default:
		// gated, busy or gone: the hardware holds the level in force again, so the ramp starts over once it is heard
		if c.ramped > 0 {
			r.vol.DropTransient()
		}
		c.sounding, c.ramped = false, 0
		r.unheard(c, codeToken(code))
	}
}

// heard reads the level an alarm's ramp starts from, the first time the ring is heard: none from a level that is not
// the device's own, nor for an alarm quieter than it. a ring heard again ramps from the same level. callers hold mu
func (r *Ringer) heard(c *ring) {
	if !c.ramps() || c.rampRead || c.quiet {
		return
	}
	c.rampRead = true
	if level, known := r.vol.Current(); known && c.Level > level {
		c.ramp, c.rampFrom = true, level
	}
}

// rampTo raises the speaker to where the ramp stands at now, timed from the ring's start; never lower than it was
// raised to, and never lower than the level in force (the volume's own check). callers hold mu
func (r *Ringer) rampTo(c *ring, now time.Time) {
	if !c.ramp {
		return
	}
	target := RampLevel(c.rampFrom, c.Level, now.Sub(c.began), c.rampFor)
	if target <= c.ramped || !c.quiet && target <= c.rampFrom {
		return
	}
	r.setLevel(c, target)
}

// setLevel puts the ring's level on the hardware: above the level in force through the ramp's raise, at or below it
// through a quiet set. callers hold mu
func (r *Ringer) setLevel(c *ring, target int) {
	set := r.vol.Quiet
	if level, known := r.vol.Current(); known && target > level {
		set = r.vol.Transient
	}
	if ok, err := set(target); err == nil && ok {
		c.ramped, c.raised = target, true
	}
}

// stop ends the ring that sounds: the ramp first, then the tone, and only then the level in force goes back on the
// hardware, once, so no step of the ramp can land after it (rule 7). the next ring waiting starts at once, but not at
// shutdown. callers hold mu
func (r *Ringer) stop(reason StopReason, now time.Time) {
	c := r.cur
	c.ramp = false
	if c.asked && r.speaker != nil {
		_ = r.speaker.StopTone(c.key)
	}
	if c.raised {
		r.vol.Reassert()
	}
	r.log.Info("ring_stop", "kind", string(c.Kind), "id", c.ID, "reason", string(reason))
	if r.counts != nil {
		r.counts.RingStopped(string(c.Kind))
	}
	if reason != StopShutdown {
		r.book.Stopped(c.Ring, reason, now)
	}
	// the next ring takes its place with no moment between them in which nothing rings
	if len(r.waiting) > 0 && reason != StopShutdown {
		next := r.waiting[0]
		r.waiting = r.waiting[1:]
		r.start(next, now, false)
		return
	}
	r.setCur(nil)
	if r.hooks.Ringing != nil {
		r.hooks.Ringing(false)
	}
	r.cap.setRinging(nil)
}

// shutdown stops the ring that sounds and misses the ones that wait; nothing rings after it
func (r *Ringer) shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if r.cur != nil {
		r.stop(StopShutdown, r.now())
	}
	for _, w := range r.waiting {
		r.missed(w)
	}
	r.waiting, r.closed = nil, true
}

func (r *Ringer) missed(w Ring) {
	r.log.Warn("ring_missed", "kind", string(w.Kind), "id", w.ID)
}

// unheard logs why the ring is not heard, once for each reason in a row. callers hold mu
func (r *Ringer) unheard(c *ring, reason string) {
	if c.unheard == reason {
		return
	}
	c.unheard = reason
	r.log.Info("ring_unheard", "kind", string(c.Kind), "id", c.ID, "reason", reason)
}

// next is how long until step has something to do: the book's next due second, the ring's limit, its next ask or the
// ramp's next step. callers hold mu
func (r *Ringer) next(now time.Time) time.Duration {
	d := r.book.NextTick(now)
	if c := r.cur; c != nil {
		d = min(d, c.began.Add(c.limit).Sub(now))
		switch {
		case !c.sounding && !c.invalid:
			d = min(d, c.retryAt.Sub(now))
		case c.sounding && c.ramp:
			if step, ok := RampNextStep(c.rampFrom, c.Level, now.Sub(c.began), c.rampFor); ok {
				d = min(d, step)
			}
		}
	}
	return max(d, minWake)
}

// codeToken keeps an audiod code to a token for the log
func codeToken(code string) string {
	if code == "" || len(code) > 32 {
		return "unknown"
	}
	for _, c := range []byte(code) {
		if (c < 'a' || c > 'z') && c != '_' {
			return "unknown"
		}
	}
	return code
}
