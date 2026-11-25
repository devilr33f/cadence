package alarmview

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/events"
)

// the outcomes of a ring that ended
const (
	OutcomeStopped = "stopped"
	OutcomeSnoozed = "snoozed"
	OutcomeMissed  = "missed"
)

// Deps are the watcher's collaborators.
type Deps struct {
	// Publish puts an event on the bus; nil publishes nothing
	Publish func(events.Event)
	// Changed hears each device whose alarms state changed, after its events; nil hears nothing. It must not block.
	Changed func(deviceID string)
	Now     func() time.Time
	Log     *slog.Logger
}

// Watcher follows the devices' reported alarms states (devices.Service.OnState): it keeps each device's last state
// and its ring's start, and publishes the alarms topic's events.
type Watcher struct {
	d Deps

	mu    sync.Mutex
	state map[string]State
	rings map[string]ringing
}

type ringing struct {
	Ring
	since time.Time
}

func NewWatcher(d Deps) *Watcher {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Watcher{d: d, state: map[string]State{}, rings: map[string]ringing{}}
}

// StateChanged is devices.Service.OnState's hook: a device's alarms state changed.
func (w *Watcher) StateChanged(deviceID, capability string, raw json.RawMessage) {
	if capability != Capability {
		return
	}
	st, ok := Parse(raw)
	if !ok {
		return
	}
	now := w.d.Now().UTC()
	w.mu.Lock()
	w.state[deviceID] = st
	prev, had := w.rings[deviceID]
	var started, ended bool
	switch {
	case st.Ringing == nil:
		ended = had
		delete(w.rings, deviceID)
	case !had || prev.Ring != *st.Ringing:
		ended, started = had, true
		w.rings[deviceID] = ringing{Ring: *st.Ringing, since: now}
	}
	w.mu.Unlock()

	w.publish(deviceID, events.AlarmsState, now, raw)
	if ended {
		w.publish(deviceID, events.AlarmRingEnded, now, endedData(prev.Ring, st, now))
		w.d.Log.Info("alarm_ring_ended", "device_id", deviceID, "kind", prev.Kind, "id", prev.ID)
	}
	if started {
		data := events.AlarmRingData{Kind: st.Ringing.Kind, ID: st.Ringing.ID, StartedAt: now}
		if it, ok := st.Find(*st.Ringing); ok {
			data.Label, data.Time = it.Label, it.Time
		}
		w.publish(deviceID, events.AlarmRinging, now, data)
		w.d.Log.Info("alarm_ringing", "device_id", deviceID, "kind", data.Kind, "id", data.ID)
	}
	if w.d.Changed != nil {
		w.d.Changed(deviceID)
	}
}

// endedData says how a ring ended from the state after it: put off when it is snoozed now, missed when it rang to
// its limit unanswered, else stopped
func endedData(r Ring, st State, now time.Time) events.AlarmRingEndedData {
	d := events.AlarmRingEndedData{Kind: r.Kind, ID: r.ID, Outcome: OutcomeStopped, At: now}
	if l := st.LastRing; l != nil && l.Ring == r {
		d.StoppedBy = l.StoppedBy
		if !l.At.IsZero() {
			d.At = l.At
		}
	}
	until, snoozed := st.SnoozedUntil(r)
	switch {
	case snoozed && until.After(now):
		d.Outcome, d.SnoozedUntil = OutcomeSnoozed, &until
	case d.StoppedBy == "timeout":
		d.Outcome = OutcomeMissed
	}
	return d
}

func (w *Watcher) publish(deviceID, typ string, at time.Time, data any) {
	if w.d.Publish != nil {
		w.d.Publish(events.Event{Type: typ, DeviceID: deviceID, At: at, Data: data})
	}
}

// Forget drops a deleted device.
func (w *Watcher) Forget(deviceID string) {
	w.mu.Lock()
	delete(w.state, deviceID)
	delete(w.rings, deviceID)
	w.mu.Unlock()
	if w.d.Changed != nil {
		w.d.Changed(deviceID)
	}
}

// Ringing is the ring that sounds on the device and when the server first heard it, false for none.
func (w *Watcher) Ringing(deviceID string) (Ring, time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.rings[deviceID]
	return r.Ring, r.since, ok
}

// Seed takes a device's stored state without an event: at a link's start, or for a device the watcher has not heard
// since the server started. A ring in it counts from now.
func (w *Watcher) Seed(deviceID string, raw json.RawMessage) {
	st, ok := Parse(raw)
	if !ok {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, known := w.state[deviceID]; known {
		return
	}
	w.state[deviceID] = st
	if st.Ringing != nil {
		w.rings[deviceID] = ringing{Ring: *st.Ringing, since: w.d.Now().UTC()}
	}
}

// State is the device's last state the watcher read, false for none.
func (w *Watcher) State(deviceID string) (State, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st, ok := w.state[deviceID]
	return st, ok
}
