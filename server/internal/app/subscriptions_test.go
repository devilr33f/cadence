package app

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/presence"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
)

type heldSpeaker struct {
	mu   sync.Mutex
	said []string
}

func (s *heldSpeaker) SessionActive(string) bool { return false }
func (s *heldSpeaker) Announcing(string) bool    { return false }
func (s *heldSpeaker) Announce(_ context.Context, _, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.said = append(s.said, text)
	return nil
}

func (s *heldSpeaker) spoken() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.said)
}

// threadModule is a module whose updates a test pushes.
type threadModule struct{ ch chan subscriptions.Update }

func (m threadModule) Module() string                  { return "t3code" }
func (m threadModule) Events() []string                { return []string{subscriptions.EventStopped} }
func (m threadModule) Validate(string, []string) error { return nil }
func (m threadModule) List(context.Context, string, []string) ([]subscriptions.Entry, error) {
	return nil, nil
}
func (m threadModule) Updates(string) <-chan subscriptions.Update { return m.ch }
func (m threadModule) Answer(context.Context, subscriptions.Ref, subscriptions.Answer) error {
	return subscriptions.ErrNoAnswer
}

// busy mode holds the hub's updates: a busy device keeps one, busy's end
// lets it go (afterBusy announce), or leaves it for the inbox (afterBusy
// inbox)
func TestHubHeldByBusyMode(t *testing.T) {
	pool := storetest.Pool(t)
	ctx := context.Background()
	q := db.New(pool)
	dev, err := q.CreateDevice(ctx, db.CreateDeviceParams{Name: "d", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	// busy mode covers enrolled devices
	if _, err := pool.Exec(ctx, `UPDATE devices SET status = 'enrolled'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO provider_instances (name, module) VALUES ('work', 't3code')`); err != nil {
		t.Fatal(err)
	}
	busy := presence.New(presence.Deps{Queries: q, Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(busy.Run(ctx))
	var mu sync.Mutex
	after := settings.AfterBusyAnnounce
	values := func(context.Context, string) (settings.Values, error) {
		mu.Lock()
		defer mu.Unlock()
		v := settings.Defaults()
		v.QuietHours.Enabled, v.AfterBusy = false, after
		return v, nil
	}
	speaker := &heldSpeaker{}
	subs := newSubscriptionHub(subscriptionDeps{
		pool: pool, settings: values, convs: conversation.New(q), online: func(string) bool { return true },
		log: slog.New(slog.DiscardHandler), busy: busy, speaker: speaker,
	})
	mod := threadModule{ch: make(chan subscriptions.Update, 4)}
	if err := subs.hub.Registry().Register(mod); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subs.run(ctx, slog.New(slog.DiscardHandler)))
	if _, err := subs.hub.Subscribe(ctx, subscriptions.Request{Topic: "t3code:work:*", Device: dev.ID.String(), CreatedBy: subscriptions.ByAPI}); err != nil {
		t.Fatal(err)
	}
	// the hub gathers updates for 5 s before it looks at the device
	slow := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	item := func(ref string) subscriptions.InboxItem {
		items, _ := subs.store.Inbox(ctx, dev.ID.String(), 50)
		for _, it := range items {
			if it.RefID == ref {
				return it
			}
		}
		return subscriptions.InboxItem{}
	}
	push := func(ref string) {
		mod.ch <- subscriptions.Update{Topic: subscriptions.NewTopic("t3code", "work", "thread", ref), Event: subscriptions.EventStopped, Kind: "thread", Ref: subscriptions.Ref{ID: ref}}
	}

	if _, err := busy.Put(ctx, "e2e", "call", nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	push("a")
	slow("held", func() bool { return item("a").HeldBusy })
	time.Sleep(time.Second)
	if n := len(speaker.spoken()); n != 0 {
		t.Fatalf("spoken while busy: %d", n)
	}
	if err := busy.Delete(ctx, "e2e"); err != nil {
		t.Fatal(err)
	}
	slow("spoken once free", func() bool { return len(speaker.spoken()) == 1 })

	mu.Lock()
	after = settings.AfterBusyInbox
	mu.Unlock()
	if _, err := busy.Put(ctx, "e2e", "call", nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	push("b")
	slow("held", func() bool { return item("b").HeldBusy })
	if err := busy.Delete(ctx, "e2e"); err != nil {
		t.Fatal(err)
	}
	slow("left for the inbox", func() bool { return item("b").State == "inbox" })
	time.Sleep(300 * time.Millisecond)
	if n := len(speaker.spoken()); n != 1 {
		t.Fatalf("spoken %d", n)
	}
}
