package devices

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/events"
)

// the capability whose state's level device_volume carries (Ruling M8-R1)
const outputCapability = "audio.output"

// presence is what the devices topic's events need besides the store: each
// device's name and model as last read, so a link's events never wait on the
// store under the hub's lifecycle lock, and the deleted devices, which get no
// event after device_deleted. Its lock orders those events with the hub's
// state.
type presence struct {
	mu    sync.Mutex
	known map[string]named
	gone  map[string]struct{}
	// the reads a link's event waits on, outside the hub's lock
	late sync.WaitGroup
}

type named struct{ name, model string }

// PublishTo makes the device events of the devices topic go to bus (M8-D2):
// a link's presence, a rename, a delete and the reported output level.
func (s *Service) PublishTo(bus events.Publisher) { s.bus = bus }

// Remember keeps the device's name and model for its link's events, as the
// link's last-seen write read them before the hub took the link.
func (s *Service) Remember(deviceID, name, model string) {
	id, ok := parseID(deviceID)
	if !ok {
		return
	}
	s.pres.mu.Lock()
	defer s.pres.mu.Unlock()
	s.rememberLocked(id.String(), named{name, model})
}

func (s *Service) rememberLocked(dev string, n named) {
	if _, gone := s.pres.gone[dev]; !gone {
		s.pres.known[dev] = n
	}
}

// Linked publishes device_online for the device whose link the hub attached;
// it runs in the link's hello, under the hub's lock, so it reads no store:
// without a remembered name it reads the device afterwards.
func (s *Service) Linked(deviceID string) { s.linkEvent(events.DeviceOnline, deviceID) }

// Unlinked publishes device_offline for the device whose link the hub let go,
// as Linked does; a deleted device gets none, its delete told it.
func (s *Service) Unlinked(deviceID string) { s.linkEvent(events.DeviceOffline, deviceID) }

// WaitPresence waits for the link events that read their device late.
func (s *Service) WaitPresence() { s.pres.late.Wait() }

func (s *Service) linkEvent(typ, deviceID string) {
	id, ok := parseID(deviceID)
	if s.bus == nil || !ok {
		return
	}
	dev := id.String()
	s.pres.mu.Lock()
	n, known := s.pres.known[dev]
	_, gone := s.pres.gone[dev]
	if known || gone {
		s.publishLink(typ, dev, n)
		s.pres.mu.Unlock()
		return
	}
	s.pres.mu.Unlock()
	s.pres.late.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.dbTimeout)
		row, err := s.q.DeviceByID(ctx, id)
		cancel()
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return
		case err != nil:
			s.log.Warn("device_presence_failed", "device_id", dev, "type", typ)
			return
		}
		s.pres.mu.Lock()
		defer s.pres.mu.Unlock()
		// a rename meanwhile told a newer name than this read
		if _, ok := s.pres.known[dev]; !ok {
			s.rememberLocked(dev, named{row.Name, row.Model})
		}
		s.publishLink(typ, dev, s.pres.known[dev])
	})
}

// publishLink publishes a link's event while the hub agrees with it: an
// online for a linked device, an offline for one that is not, so an event
// read late never contradicts a newer one. Run with presence's lock held.
func (s *Service) publishLink(typ, dev string, n named) {
	if _, gone := s.pres.gone[dev]; gone {
		return
	}
	online := s.hub.Online(dev)
	if online != (typ == events.DeviceOnline) {
		return
	}
	s.bus.Publish(events.Event{Type: typ, DeviceID: dev, At: s.now(),
		Data: events.DeviceData{Name: n.name, Model: n.model, Online: online}})
}

// renamed remembers the device's new name and publishes device_renamed.
func (s *Service) renamed(dev, name, model string) {
	s.pres.mu.Lock()
	defer s.pres.mu.Unlock()
	s.rememberLocked(dev, named{name, model})
	if _, gone := s.pres.gone[dev]; gone || s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{Type: events.DeviceRenamed, DeviceID: dev, At: s.now(),
		Data: events.DeviceData{Name: name, Model: model, Online: s.hub.Online(dev)}})
}

// deleted publishes device_deleted; no event of the device follows it.
func (s *Service) deleted(dev string) {
	s.pres.mu.Lock()
	defer s.pres.mu.Unlock()
	delete(s.pres.known, dev)
	s.pres.gone[dev] = struct{}{}
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: events.DeviceDeleted, DeviceID: dev, At: s.now()})
	}
}

// publishVolume publishes device_volume when the audio.output state now stored
// reports another level than the one before it, or the first.
func (s *Service) publishVolume(dev string, before, after []byte) {
	if s.bus == nil {
		return
	}
	now, ok := outputLevel(after)
	if !ok {
		return
	}
	if was, known := outputLevel(before); known && was == now {
		return
	}
	s.bus.Publish(events.Event{Type: events.DeviceVolume, DeviceID: dev, At: s.now(), Data: events.VolumeData{Level: now}})
}

func outputLevel(state []byte) (int, bool) {
	var v struct {
		Level *int `json:"level"`
	}
	if len(state) == 0 || json.Unmarshal(state, &v) != nil || v.Level == nil {
		return 0, false
	}
	return *v.Level, true
}
