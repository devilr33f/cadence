package presence_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/presence"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// fakeHome is Home Assistant's states, up or down; every change wakes Watch.
type fakeHome struct {
	mu     sync.Mutex
	up     bool
	states map[string]string
	next   chan struct{}
}

func newFakeHome() *fakeHome {
	return &fakeHome{states: map[string]string{}, next: make(chan struct{})}
}

func (h *fakeHome) Watch(ids []string) (bool, map[string]string, <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.up {
		return false, nil, h.next
	}
	out := map[string]string{}
	for _, id := range ids {
		if s, ok := h.states[id]; ok {
			out[id] = s
		}
	}
	return true, out, h.next
}

func (h *fakeHome) set(edit func(h *fakeHome)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	edit(h)
	close(h.next)
	h.next = make(chan struct{})
}

type entities struct {
	mu   sync.Mutex
	list []settings.BusyEntity
	err  error
}

func (e *entities) get(context.Context) ([]settings.BusyEntity, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list), e.err
}

// a matching state makes every device busy with its reason while it holds
// and Home Assistant is connected; a disconnect or a changed setting clears it
func TestHomeAssistantStatesMakeDevicesBusy(t *testing.T) {
	r := newRig(t)
	r.run(r.s)
	home := newFakeHome()
	home.set(func(h *fakeHome) { h.up, h.states["sensor.phone_state"] = true, "idle" })
	ents := &entities{list: []settings.BusyEntity{
		{Entity: "sensor.phone_state", State: "offhook", Reason: "call"},
		{Entity: "sensor.phone_state", State: "ringing", Reason: "call"},
		{Entity: "binary_sensor.mac_audio_input_in_use", State: "on", Reason: "meeting"},
	}}
	poke, stop := r.s.RunHA(context.Background(), presence.HADeps{Home: home, Entities: ents.get})
	defer stop()
	busy := func(id string) func() bool { return func() bool { return r.busy(id) != "" } }
	r.changed()
	home.set(func(h *fakeHome) { h.states["sensor.phone_state"] = "ringing" })
	eventually(t, "the call", busy(r.a))
	if r.busy(r.a) != "call" || r.busy(r.b) != "call" {
		t.Fatalf("a %q b %q", r.busy(r.a), r.busy(r.b))
	}
	sigs := r.s.Signals()
	if len(sigs) != 1 || sigs[0].Source != "ha:sensor.phone_state" || sigs[0].Devices != nil || !sigs[0].ExpiresAt.IsZero() {
		t.Fatalf("signals %+v", sigs)
	}
	home.set(func(h *fakeHome) {
		h.states["sensor.phone_state"] = "idle"
		h.states["binary_sensor.mac_audio_input_in_use"] = "on"
	})
	eventually(t, "the meeting", func() bool { return r.busy(r.a) == "meeting" })
	// a stored signal and a live one together: the call wins
	r.put("dashboard", presence.ReasonDND, nil, 60e9)
	if r.busy(r.b) != "meeting" {
		t.Fatalf("b %q", r.busy(r.b))
	}
	if err := r.s.Delete(context.Background(), "dashboard"); err != nil {
		t.Fatal(err)
	}
	// Home Assistant goes: its signals go with it
	home.set(func(h *fakeHome) { h.up = false })
	eventually(t, "the disconnect", func() bool { return !busy(r.a)() })
	home.set(func(h *fakeHome) { h.up = true })
	eventually(t, "the reconnect", busy(r.a))
	// the setting no longer names the entity
	ents.mu.Lock()
	ents.list = ents.list[:2]
	ents.mu.Unlock()
	poke()
	eventually(t, "the setting", func() bool { return !busy(r.a)() })
	noEntity(t, r.logs.String())
}

// settings that do not read keep what was matched; stop clears the signals
func TestHomeAssistantSourceStops(t *testing.T) {
	r := newRig(t)
	r.run(r.s)
	home := newFakeHome()
	home.set(func(h *fakeHome) { h.up, h.states["sensor.phone_state"] = true, "offhook" })
	ents := &entities{list: []settings.BusyEntity{{Entity: "sensor.phone_state", State: "offhook", Reason: "call"}}}
	poke, stop := r.s.RunHA(context.Background(), presence.HADeps{Home: home, Entities: ents.get})
	eventually(t, "the call", func() bool { return r.busy(r.a) == "call" })
	ents.mu.Lock()
	ents.err = errors.New("store down")
	ents.mu.Unlock()
	poke()
	home.set(func(*fakeHome) {})
	if r.busy(r.a) != "call" {
		t.Fatal("an unreadable setting cleared the call")
	}
	stop()
	if r.busy(r.a) != "" || len(r.s.Signals()) != 0 {
		t.Fatal("stop left the signal")
	}
	noEntity(t, r.logs.String())
}
