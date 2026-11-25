package subscriptions

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// Voice is the devices' voice side, as the task reporter reads it.
type Voice interface {
	SessionActive(deviceID string) bool
	Announcing(deviceID string) bool
	// Announce speaks text on the device outside any session; nil once its
	// first audio went out.
	Announce(ctx context.Context, deviceID, text string) error
}

// EventTurn is the input of an agent delivery's event turn: Text is fixed
// framing of our own, Data the updates as the outside systems wrote them,
// which the turn reads as untrusted tool output.
type EventTurn struct {
	DeviceID string
	Text     string
	Data     json.RawMessage
}

// Turns runs event turns: what the turn says, "" when it chose silence.
type Turns interface {
	RunUpdate(ctx context.Context, e EventTurn) (string, error)
}

// Notices writes a spoken update into the device's conversation history as
// an assistant notice: no user message, no turn loop.
type Notices func(ctx context.Context, deviceID, text string) error

type Deps struct {
	Store    *Store
	Registry *Registry
	Voice    Voice
	// Turns runs agent deliveries; nil speaks them as speaker ones
	Turns   Turns
	Notices Notices
	Online  func(deviceID string) bool
	// Settings resolves a device's settings: language, quiet hours, the
	// subscription defaults
	Settings func(ctx context.Context, deviceID string) (settings.Values, error)
	// Busy holds unasked speech; nil is never busy
	Busy Busy
	// AfterBusy reads a device's afterBusy setting; nil is announce
	AfterBusy AfterBusy
	Bus       events.Publisher
	Log       *slog.Logger
	Now       func() time.Time
	// Awake is how long a session the user began counts as them awake in
	// quiet hours; zero keeps 2 min
	Awake time.Duration
}

// the live feed's events (events.Subscription*): ids, topics, events and
// states, never an outside text
const (
	EventUpdate     = events.SubscriptionUpdate
	EventChangedSub = events.SubscriptionChanged
	EventInbox      = events.SubscriptionInbox
)

type limits struct {
	// window gathers the updates of a few seconds into one announcement
	window time.Duration
	// settle follows a session's end or a new link
	settle time.Duration
	awake  time.Duration
	sweep  time.Duration
	// backoff doubles up to maxBackoff after a failed delivery
	backoff, maxBackoff time.Duration
	turn                time.Duration
}

var defaultLimits = limits{
	window: 5 * time.Second, settle: 2 * time.Second, awake: 2 * time.Minute, sweep: time.Minute,
	backoff: 15 * time.Second, maxBackoff: 5 * time.Minute, turn: 3 * time.Minute,
}

// Hub matches the modules' updates to the devices' subscriptions, stores
// them and delivers them once each device can take them. Each device has
// one worker, so its deliveries never overlap.
type Hub struct {
	d   Deps
	lim limits

	mu    sync.Mutex
	ctx   context.Context
	wg    sync.WaitGroup
	devs  map[string]*device
	pumps map[instanceKey]bool
}

func New(d Deps) *Hub {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Registry == nil {
		d.Registry = NewRegistry()
	}
	lim := defaultLimits
	if d.Awake > 0 {
		lim.awake = d.Awake
	}
	h := &Hub{d: d, lim: lim, devs: map[string]*device{}, pumps: map[instanceKey]bool{}}
	d.Registry.onAdd(h.reconcile)
	return h
}

// Registry is the hub's modules.
func (h *Hub) Registry() *Registry { return h.d.Registry }

// Run follows the modules and delivers until ctx ends; stop waits for the
// workers.
func (h *Hub) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.ctx = ctx
	for _, d := range h.devs {
		h.startLocked(d)
	}
	h.mu.Unlock()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		tick := time.NewTicker(h.lim.sweep)
		defer tick.Stop()
		for {
			h.sweep(ctx)
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return func() { cancel(); h.wg.Wait() }
}

// sweep opens the streams subscriptions name, and looks at every device with
// updates waiting: quiet hours that ended, a missed kick.
func (h *Hub) sweep(ctx context.Context) {
	h.reconcile()
	devs, err := h.d.Store.pendingDevices(ctx)
	if err != nil {
		h.failed(ctx, "sweep", err)
		return
	}
	for _, id := range devs {
		h.kick(id, false)
	}
}

// Refresh opens the streams of instances a module can serve now (an
// instance its settings started): what reconcile does at a sweep, at once.
func (h *Hub) Refresh() { go h.reconcile() }

// Followed is the topics of the subscriptions naming a module's instance
// that deliver (mute ones left out), so a module can follow in detail only
// what some subscription could match.
func (h *Hub) Followed(ctx context.Context, module, instance string) ([]Topic, error) {
	subs, err := h.d.Store.ofInstance(ctx, module, instance)
	if err != nil {
		return nil, err
	}
	var out []Topic
	for _, s := range subs {
		if s.Delivery != DeliveryMute {
			out = append(out, s.topic)
		}
	}
	return out, nil
}

// reconcile opens the stream of every subscribed instance whose module is
// registered and has none open.
func (h *Hub) reconcile() {
	h.mu.Lock()
	ctx := h.ctx
	h.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	keys, err := h.d.Store.instances(ctx)
	if err != nil {
		h.failed(ctx, "instances", err)
		return
	}
	for _, k := range keys {
		m, ok := h.d.Registry.Get(k.module)
		if !ok {
			continue
		}
		h.mu.Lock()
		if h.pumps[k] {
			h.mu.Unlock()
			continue
		}
		h.pumps[k] = true
		h.wg.Add(1)
		h.mu.Unlock()
		go h.pump(ctx, m, k)
	}
}

// pump reads one instance's stream until it closes or ctx ends.
func (h *Hub) pump(ctx context.Context, m Subscribable, k instanceKey) {
	defer h.wg.Done()
	defer func() {
		h.mu.Lock()
		delete(h.pumps, k)
		h.mu.Unlock()
	}()
	ch := m.Updates(k.instance)
	if ch == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-ch:
			if !ok {
				return
			}
			h.Ingest(ctx, m, u)
		}
	}
}

var (
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	codeRe  = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// ends reports an event after which a thing asks nothing more.
func ends(ev string) bool { return ev == EventStopped || ev == EventDone || ev == EventError }

// Ingest takes one update of module m: it withdraws the questions it ends,
// then stores it for each device whose deciding subscription asks for it,
// ending a next_stop subscription on its stop, and kicks those devices.
func (h *Hub) Ingest(ctx context.Context, m Subscribable, u Update) {
	mod := m.Module()
	// a module answers within the instance the update came from
	u.Ref.Instance = u.Topic.Instance
	if u.Withdraw && u.Event == "" && u.Question == nil && u.Topic.Module == mod && !u.Topic.Wild {
		// a withdrawal alone: a question answered or ended elsewhere
		if err := h.d.Store.withdraw(ctx, mod, u.Ref, ""); err != nil {
			h.failed(ctx, "withdraw", err)
		}
		return
	}
	if u.Topic.Module != mod || u.Topic.Wild || !slices.Contains(m.Events(), u.Event) {
		h.d.Log.Warn("subscription_update_refused", "module", mod, "event", codeOf(u.Event))
		return
	}
	if u.At.IsZero() {
		u.At = h.d.Now()
	}
	if u.Question != nil && (u.Question.Token == "" || !tokenRe.MatchString(u.Question.Token)) {
		// an answer could not be tied to the prompt
		u.Question = nil
	}
	keep := ""
	if u.Question != nil {
		keep = u.Question.Token
	}
	if u.Withdraw || u.Question != nil || ends(u.Event) {
		if err := h.d.Store.withdraw(ctx, mod, u.Ref, keep); err != nil {
			h.failed(ctx, "withdraw", err)
		}
	}
	subs, err := h.d.Store.ofInstance(ctx, mod, u.Topic.Instance)
	if err != nil {
		h.failed(ctx, "ingest", err)
		return
	}
	for _, mt := range match(subs, u) {
		h.add(ctx, mod, u, mt)
	}
}

func (h *Hub) add(ctx context.Context, mod string, u Update, mt Match) {
	delivery := mt.Sub.Delivery
	if delivery == DeliveryAgent && h.d.Turns == nil {
		delivery = DeliverySpeaker
	}
	it := InboxItem{
		Device: mt.Device, SubscriptionID: mt.Sub.ID, Module: mod, Instance: u.Topic.Instance, Topic: u.Topic.String(),
		Event: u.Event, Kind: u.Kind, Title: u.Title, Text: u.Text, RefID: u.Ref.ID, Question: u.Question,
		Delivery: delivery, State: statePending, At: u.At,
	}
	end := mt.Sub.Until == UntilNextStop && u.Event == EventStopped
	id, err := h.d.Store.add(ctx, it, end)
	if err != nil {
		h.failed(ctx, "add", err)
		return
	}
	h.publish(mt.Device, EventUpdate, map[string]any{"id": id, "topic": it.Topic, "event": it.Event, "state": statePending})
	if end {
		h.publish(mt.Device, EventChangedSub, map[string]any{"id": mt.Sub.ID, "topic": mt.Sub.Topic, "action": "ended"})
	}
	h.d.Log.Info("subscription_update", "module", mod, "event", u.Event, "device_id", mt.Device, "item_id", id, "delivery", delivery)
	h.queued(mt.Device)
}

func (h *Hub) publish(device, typ string, data map[string]any) {
	if h.d.Bus == nil {
		return
	}
	h.d.Bus.Publish(events.Event{Type: typ, DeviceID: device, At: h.d.Now(), Data: data})
}

func (h *Hub) failed(ctx context.Context, op string, err error) {
	if ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		h.d.Log.Error("subscription_failed", "op", op)
	}
}
