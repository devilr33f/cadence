// Package remote shows a screen what the household's other devices play and lets it control them: devices.media is a
// home strip's json list (and the top bar's indicator), devices.panel the quick panel's cadence cards with each
// session's metadata, transport and position (a seek where the session lists it), a volume slider per device and
// «play here too» (the screen joins the playing device's group) or «stop here» (it leaves). Both are subscribed and answer again after a change, coalesced.
//
// What a session plays (titles, artists, covers) and the owner's device and room names are memory-only (D11) and
// never logged (rule 8): lines carry ids, codes and counts. A volume moves only by a slider's press on a screen, an
// authenticated user's explicit action, never by the server on its own (rule 7).
package remote

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

// settle coalesces a burst of changes into one answer
const settle = 250 * time.Millisecond

// Device is an enrolled device: its owner-set name and room ("" for none).
type Device struct{ ID, Name, Room string }

// Group is a multiroom group: its elected leader ("" while none is) and its members in order.
type Group struct {
	ID, Name, Kind, Leader string
	Members                []string
}

// Deps are the service's collaborators.
type Deps struct {
	Devices func(ctx context.Context) ([]Device, error)
	Groups  func(ctx context.Context) ([]Group, error)
	// Session is a device's media session (media.Store.Get), false for none
	Session func(deviceID string) (media.MediaSession, bool)
	// Level is a device's audio.output level as it last reported it, false when unknown
	Level func(ctx context.Context, deviceID string) (int, bool)
	// Groupable says the device takes part in multiroom groups (it reports media.group)
	Groupable func(ctx context.Context, deviceID string) bool
	// Art is a session's cover as an img: ref through the image proxy, "" for none
	Art func(deviceID string, s media.MediaSession) string
	// Command runs a capability command on a device (commands.Service.Send)
	Command func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
	// Join makes lead lead a group with others (groups.Service.Join); Attach adds deviceID to member's group without
	// moving its lead; Leave takes deviceID out of its group
	Join   func(ctx context.Context, lead string, others []string) error
	Attach func(ctx context.Context, member, deviceID string) error
	Leave  func(ctx context.Context, deviceID string) error
	// Cards renders the panel's pages and issues their actions
	Cards interface {
		Page(ctx context.Context, deviceID string, p *cards.Panel) (*cardsv1.Page, error)
	}
	// CardFeatures are the card features the screen's hello listed (display.screen's cardFeatures), nil for none: a
	// card's position slider goes only to a screen that lists slider.timeline
	CardFeatures func(ctx context.Context, deviceID string) []string
	// Now is the clock a position without the device's stamp is anchored at (time.Now when nil)
	Now func() time.Time
	Log *slog.Logger
}

// Service serves devices.media and devices.panel.
type Service struct {
	d  Deps
	ch *changes
}

func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d, ch: newChanges()}
}

// watched are the events that may change what a screen is shown: a session, a group, a device's name, room, volume
// or link
var watched = map[string]bool{
	events.MediaState: true, events.GroupChanged: true, events.DeviceRenamed: true, events.DeviceDeleted: true,
	events.DeviceVolume: true, events.DeviceOnline: true, events.DeviceOffline: true,
}

// Run follows the bus until ctx ends or stop is called: each watched event wakes the screens' subscriptions.
func (s *Service) Run(ctx context.Context, bus *events.Bus) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	sub := bus.Watch(func(e events.Event) bool { return watched[e.Type] })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.C:
				s.ch.changed()
			case <-sub.Drops():
				s.ch.changed()
			}
		}
	}()
	return func() { cancel(); <-done; sub.Close() }
}

// Changed wakes the subscriptions, as an event would.
func (s *Service) Changed() { s.ch.changed() }

// changes is closed at each change and made anew
type changes struct {
	mu sync.Mutex
	c  chan struct{}
}

func newChanges() *changes { return &changes{c: make(chan struct{})} }

func (c *changes) of() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.c
}

func (c *changes) changed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	close(c.c)
	c.c = make(chan struct{})
}

// next waits for ch, then for the burst to settle; false once ctx ends
func next(ctx context.Context, ch <-chan struct{}) bool {
	select {
	case <-ch:
	case <-ctx.Done():
		return false
	}
	select {
	case <-time.After(settle):
		return true
	case <-ctx.Done():
		return false
	}
}
