package subscriptions

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

// fakeModule is a Subscribable: its updates come from push, its answers are
// recorded.
type fakeModule struct {
	name    string
	mu      sync.Mutex
	streams map[string]chan Update
	answers []fakeAnswer
	failAns error
	entries []Entry
}

type fakeAnswer struct {
	Ref Ref
	A   Answer
}

func newFakeModule() *fakeModule {
	return &fakeModule{name: "t3code", streams: map[string]chan Update{}}
}

func (m *fakeModule) Module() string { return m.name }

func (m *fakeModule) Events() []string {
	if m.name != "t3code" {
		return []string{EventChanged}
	}
	return []string{EventStopped, EventQuestion, EventApproval, EventError}
}

func (m *fakeModule) Validate(_ string, path []string) error {
	if len(path) > 0 && path[0] != "thread" && path[0] != "project" {
		return errors.New("no such path")
	}
	return nil
}

func (m *fakeModule) List(_ context.Context, _ string, prefix []string) ([]Entry, error) {
	var out []Entry
	for _, e := range m.entries {
		if t, err := ParseTopic(e.Topic); err == nil && t.HasPrefix(prefix) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *fakeModule) stream(instance string) chan Update {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := m.streams[instance]
	if ch == nil {
		ch = make(chan Update, 16)
		m.streams[instance] = ch
	}
	return ch
}

func (m *fakeModule) Updates(instance string) <-chan Update { return m.stream(instance) }

func (m *fakeModule) Answer(_ context.Context, ref Ref, a Answer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAns != nil {
		return m.failAns
	}
	m.answers = append(m.answers, fakeAnswer{ref, a})
	return nil
}

func (m *fakeModule) answered() []fakeAnswer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.answers)
}

// fakeVoice is the voice side: online, idle, and every announcement recorded,
// which ends at once unless hold is set.
type fakeVoice struct {
	mu         sync.Mutex
	session    bool
	announcing bool
	fail       error
	said       []string
	hub        *Hub
}

func (v *fakeVoice) SessionActive(string) bool { v.mu.Lock(); defer v.mu.Unlock(); return v.session }
func (v *fakeVoice) Announcing(string) bool    { v.mu.Lock(); defer v.mu.Unlock(); return v.announcing }

func (v *fakeVoice) Announce(_ context.Context, dev, text string) error {
	v.mu.Lock()
	if v.fail != nil {
		v.mu.Unlock()
		return v.fail
	}
	v.said = append(v.said, text)
	v.mu.Unlock()
	// the announcement ends at once
	go v.hub.AnnounceEnded(dev)
	return nil
}

func (r *rig) setSession(on bool) {
	r.voice.mu.Lock()
	r.voice.session = on
	r.voice.mu.Unlock()
}

func (v *fakeVoice) spoken() []string { v.mu.Lock(); defer v.mu.Unlock(); return slices.Clone(v.said) }

type fakeBusy struct {
	mu   sync.Mutex
	busy bool
}

func (b *fakeBusy) Busy(context.Context, string) (bool, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.busy, "call"
}

func (b *fakeBusy) set(v bool) { b.mu.Lock(); b.busy = v; b.mu.Unlock() }

type fakeTurns struct {
	mu   sync.Mutex
	say  string
	err  error
	runs []EventTurn
}

func (f *fakeTurns) RunUpdate(_ context.Context, e EventTurn) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, e)
	return f.say, f.err
}

func (f *fakeTurns) all() []EventTurn { f.mu.Lock(); defer f.mu.Unlock(); return slices.Clone(f.runs) }

type rig struct {
	t      *testing.T
	ctx    context.Context
	hub    *Hub
	store  *Store
	mod    *fakeModule
	voice  *fakeVoice
	busy   *fakeBusy
	turns  *fakeTurns
	convs  *conversation.Store
	bus    *events.Bus
	feed   <-chan events.Event
	dev    string
	values settings.Values
	after  string
	mu     sync.Mutex
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := storetest.Pool(t)
	q := db.New(pool)
	ctx := context.Background()
	d, err := q.CreateDevice(ctx, db.CreateDeviceParams{Name: "d", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO provider_instances (name, module) VALUES ('work', 't3code'), ('home', 'homeassistant')`); err != nil {
		t.Fatal(err)
	}
	r := &rig{
		t: t, ctx: ctx, store: NewStore(pool), mod: newFakeModule(), voice: &fakeVoice{}, busy: &fakeBusy{},
		turns: &fakeTurns{}, convs: conversation.New(q), bus: events.NewBus(1024), dev: d.ID.String(),
		values: settings.Defaults(), after: AfterBusyAnnounce,
	}
	r.values.QuietHours.Enabled = false
	var stop func()
	r.feed, stop = r.bus.Subscribe(nil)
	t.Cleanup(stop)
	r.hub = New(Deps{
		Store: r.store, Voice: r.voice, Turns: r.turns, Notices: r.convs.Notice,
		Online: func(string) bool { return true },
		Settings: func(context.Context, string) (settings.Values, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.values, nil
		},
		Busy:      r.busy,
		AfterBusy: func(context.Context, string) string { r.mu.Lock(); defer r.mu.Unlock(); return r.after },
		Bus:       r.bus,
	})
	r.hub.lim.window, r.hub.lim.settle, r.hub.lim.backoff = 300*time.Millisecond, 0, 50*time.Millisecond
	r.hub.lim.sweep = time.Hour
	r.voice.hub = r.hub
	if err := r.hub.Registry().Register(r.mod); err != nil {
		t.Fatal(err)
	}
	stopHub := r.hub.Run(ctx)
	t.Cleanup(stopHub)
	return r
}

func (r *rig) settings(fn func(v *settings.Values)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(&r.values)
}

func (r *rig) subscribe(topic, delivery, until string, events ...string) Subscription {
	r.t.Helper()
	s, err := r.hub.Subscribe(r.ctx, Request{Topic: topic, Events: events, Delivery: delivery, Until: until, Device: r.dev, CreatedBy: ByAPI})
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

// push sends an update on the work instance's stream and waits for the hub
// to store it.
func (r *rig) push(u Update) {
	r.t.Helper()
	before := r.count()
	r.mod.stream("work") <- u
	r.eventually("update stored", func() bool { return r.count() > before })
}

// pushNone sends an update that no subscription takes: nothing is stored.
func (r *rig) pushNone(u Update) {
	r.t.Helper()
	before := r.count()
	r.mod.stream("work") <- u
	time.Sleep(200 * time.Millisecond)
	if r.count() != before {
		r.t.Fatal("stored")
	}
}

func (r *rig) count() int {
	var n int
	if err := r.store.pool.QueryRow(r.ctx, `SELECT count(*) FROM subscription_items`).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) inbox() []InboxItem {
	r.t.Helper()
	items, err := r.store.Inbox(r.ctx, r.dev, 100)
	if err != nil {
		r.t.Fatal(err)
	}
	return items
}

func (r *rig) eventually(what string, ok func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// quiet keeps the hub from speaking for d: nothing more is said meanwhile
func (r *rig) quiet(d time.Duration, n int) {
	r.t.Helper()
	time.Sleep(d)
	if got := r.voice.spoken(); len(got) != n {
		r.t.Fatalf("spoken %q, want %d", got, n)
	}
}

func thread(id string) Topic { return NewTopic("t3code", "work", "thread", id) }
