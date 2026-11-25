package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/presence"
	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
	"git.shino.boo/devilreef/cadence/server/internal/voice"
)

// subscriptionHub is the subscriptions hub and what it needs from parts built
// after it: the runner (its agent deliveries) and the voice coordinator (its
// announcements), set by wire. Modules register on its registry as their
// settings name them (t3code).
type subscriptionHub struct {
	hub   *subscriptions.Hub
	store *subscriptions.Store
	turns *lateTurns
	voice *announcer
}

type subscriptionDeps struct {
	pool     *pgxpool.Pool
	settings tools.SettingsFunc
	convs    *conversation.Store
	online   func(deviceID string) bool
	bus      events.Publisher
	log      *slog.Logger
	awake    time.Duration
	// busy holds unasked updates; its changes kick the hub, so what it held
	// goes once a device is free
	busy *presence.Service
	// speaker, when set, speaks in place of the voice coordinator (tests)
	speaker subscriptions.Voice
}

func newSubscriptionHub(d subscriptionDeps) *subscriptionHub {
	s := &subscriptionHub{store: subscriptions.NewStore(d.pool), turns: &lateTurns{}, voice: &announcer{}}
	var spoken subscriptions.Voice = s.voice
	if d.speaker != nil {
		spoken = d.speaker
	}
	deps := subscriptions.Deps{
		Store: s.store, Voice: spoken, Turns: s.turns, Notices: d.convs.Notice, Online: d.online,
		Settings: d.settings, Bus: d.bus, Log: d.log, Awake: d.awake, AfterBusy: afterBusy(d.settings),
	}
	if d.busy != nil {
		deps.Busy = d.busy
	}
	s.hub = subscriptions.New(deps)
	if d.busy != nil {
		d.busy.OnChange(s.hub.BusyChanged)
	}
	return s
}

func (s *subscriptionHub) tools(settings tools.SettingsFunc) []agent.Tool {
	return tools.Subscriptions(tools.SubscriptionDeps{Hub: s.hub, Settings: settings})
}

// wire sets what the hub delivers through once it exists.
func (s *subscriptionHub) wire(runner *agent.Runner, voices *voice.Coordinator) {
	s.turns.r, s.voice.c = runner, voices
}

// run starts the hub and the retention of its updates; stop waits for both.
func (s *subscriptionHub) run(ctx context.Context, log *slog.Logger) (stop func()) {
	stopHub := s.hub.Run(ctx)
	stopPrune := s.store.RunRetention(ctx, log, time.Hour)
	return func() { stopHub(); stopPrune() }
}

// afterBusy reads a device's afterBusy setting for the hub; a read that
// fails announces, as the default does.
func afterBusy(get tools.SettingsFunc) subscriptions.AfterBusy {
	return func(ctx context.Context, deviceID string) string {
		v, err := get(ctx, deviceID)
		if err == nil && v.InboxAfterBusy() {
			return subscriptions.AfterBusyInbox
		}
		return subscriptions.AfterBusyAnnounce
	}
}

// lateTurns is the runner, set once it exists.
type lateTurns struct{ r *agent.Runner }

func (t *lateTurns) RunUpdate(ctx context.Context, e subscriptions.EventTurn) (string, error) {
	return t.r.RunUpdate(ctx, e)
}

// sessionEnded tells each listener a session ended.
func sessionEnded(fns ...func(string, cadencev2.Trigger, bool)) func(string, cadencev2.Trigger, bool) {
	return func(id string, trigger cadencev2.Trigger, heard bool) {
		for _, fn := range fns {
			fn(id, trigger, heard)
		}
	}
}

// eachDevice tells each listener about a device.
func eachDevice(fns ...func(string)) func(string) {
	return func(id string) {
		for _, fn := range fns {
			fn(id)
		}
	}
}
