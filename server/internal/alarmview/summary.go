package alarmview

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync/atomic"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// DefaultSnoozeMinutes is the device's snooze when its alarms setting names none.
const DefaultSnoozeMinutes = 9

// Device is an enrolled device: its owner-set name and room ("" for none; a room's name is its id).
type Device struct{ ID, Name, Room string }

// SummaryDeps are what the summaries read.
type SummaryDeps struct {
	Devices func(ctx context.Context) ([]Device, error)
	// State is a device's stored capability state (devices.Service.State), for a device the watcher has not heard
	State func(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)
	// Online says a device is linked now: an offline device's ring shows nowhere, as no press could reach it
	Online func(deviceID string) bool
	// Desired is a device's desired capability config (devices.Service.Desired), for its snoozeMinutes
	Desired func(ctx context.Context, deviceID, capability string) (json.RawMessage, error)
}

// Summaries builds each screen's AlarmsSummary from the watcher's states.
type Summaries struct {
	w        *Watcher
	d        SummaryDeps
	revision atomic.Uint64
}

func NewSummaries(w *Watcher, d SummaryDeps) *Summaries { return &Summaries{w: w, d: d} }

// Summary is the household's alarms as the screen shows them: every device that reports alarms (the screen first,
// then its room's, then by name) and the rings it shows, its own and its room's online devices'.
func (s *Summaries) Summary(ctx context.Context, screen string) (*cadencev2.AlarmsSummary, error) {
	devs, err := s.d.Devices(ctx)
	if err != nil {
		return nil, err
	}
	room := ""
	for _, d := range devs {
		if d.ID == screen {
			room = d.Room
		}
	}
	out := &cadencev2.AlarmsSummary{}
	type entry struct {
		dev   Device
		order int
	}
	var shown []entry
	for _, d := range devs {
		st, ok := s.state(ctx, d.ID)
		if !ok {
			continue
		}
		here, same := d.ID == screen, room != "" && d.Room == room
		snooze := s.snoozeMinutes(ctx, d.ID)
		ad := &cadencev2.AlarmsDevice{DeviceId: d.ID, Name: d.Name, RoomName: d.Room, Here: here, SameRoom: same,
			SnoozeMinutes: snooze}
		for _, it := range st.Items {
			if fits(it.Kind, it.ID) {
				ad.Items = append(ad.Items, item(it))
			}
		}
		out.Devices = append(out.Devices, ad)
		order := 2
		switch {
		case here:
			order = 0
		case same:
			order = 1
		}
		shown = append(shown, entry{dev: d, order: order})
		if r, since, ok := s.w.Ringing(d.ID); ok && (here || same) && fits(r.Kind, r.ID) &&
			(s.d.Online == nil || s.d.Online(d.ID)) {
			ring := &cadencev2.AlarmRing{DeviceId: d.ID, DeviceName: d.Name, RoomName: d.Room, Kind: r.Kind, Id: r.ID,
				StartedUnixMs: since.UnixMilli(), Own: here, SnoozeMinutes: snooze}
			if it, ok := st.Find(r); ok {
				ring.Label, ring.Time, ring.Seconds = it.Label, clock(it.Time), uint32(max(it.Seconds, 0)) //nolint:gosec // a timer's length, at most a day
			}
			out.Ringing = append(out.Ringing, ring)
		}
	}
	idx := map[string]int{}
	for i, e := range shown {
		idx[e.dev.ID] = i
	}
	sort.SliceStable(out.Devices, func(i, j int) bool {
		a, b := shown[idx[out.Devices[i].GetDeviceId()]], shown[idx[out.Devices[j].GetDeviceId()]]
		if a.order != b.order {
			return a.order < b.order
		}
		return strings.ToLower(a.dev.Name) < strings.ToLower(b.dev.Name)
	})
	sort.SliceStable(out.Ringing, func(i, j int) bool { return out.Ringing[i].GetOwn() && !out.Ringing[j].GetOwn() })
	if len(out.Ringing) > maxRings {
		out.Ringing = out.Ringing[:maxRings]
	}
	out.Revision = s.revision.Add(1)
	return out, nil
}

func (s *Summaries) state(ctx context.Context, dev string) (State, bool) {
	if st, ok := s.w.State(dev); ok {
		return st, true
	}
	if s.d.State == nil {
		return State{}, false
	}
	raw, ok := s.d.State(ctx, dev, Capability)
	if !ok {
		return State{}, false
	}
	s.w.Seed(dev, raw)
	return s.w.State(dev)
}

func (s *Summaries) snoozeMinutes(ctx context.Context, dev string) uint32 {
	if s.d.Desired == nil {
		return DefaultSnoozeMinutes
	}
	raw, err := s.d.Desired(ctx, dev, Capability)
	if err != nil {
		return DefaultSnoozeMinutes
	}
	var cfg struct {
		SnoozeMinutes int `json:"snoozeMinutes"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.SnoozeMinutes < 1 || cfg.SnoozeMinutes > 60 {
		return DefaultSnoozeMinutes
	}
	return uint32(cfg.SnoozeMinutes) //nolint:gosec // 1..60
}

// the screen's nanopb bounds (proto/cadence/screen/v1/screen.options): a summary past them would not decode there
const (
	maxRings  = 4
	maxToken  = 16
	clockSize = 5
	dayName   = 3
	maxDays   = 7
)

// fits says a kind and id are within the screen's bounds; an item past them is left out
func fits(kind, id string) bool {
	return kind != "" && id != "" && len(kind) <= maxToken && len(id) <= maxToken
}

// clock is an HH:MM, "" for anything longer
func clock(t string) string {
	if len(t) > clockSize {
		return ""
	}
	return t
}

func days(in []string) []string {
	var out []string
	for _, d := range in {
		if len(d) == dayName && len(out) < maxDays {
			out = append(out, d)
		}
	}
	return out
}

func item(it Item) *cadencev2.AlarmItem {
	out := &cadencev2.AlarmItem{Kind: it.Kind, Id: it.ID, Label: it.Label, Time: clock(it.Time), Days: days(it.Days),
		Seconds: uint32(max(it.Seconds, 0)), Snoozed: it.Snoozed} //nolint:gosec // a timer's length, at most a day
	if !it.Next.IsZero() {
		out.NextUnixMs = it.Next.UnixMilli()
	}
	return out
}
