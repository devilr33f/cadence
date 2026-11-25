package ha_test

import (
	"testing"
	"time"
)

// a watcher reads the states it asks for while the connection is up, and is
// woken by a change and by a disconnect, after which it sees none
func TestHomeWatch(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	if up, states, _ := h.home.Watch([]string{"light.kitchen"}); up || states != nil {
		t.Fatalf("before the load: %v %v", up, states)
	}
	h.loaded()
	up, states, next := h.home.Watch([]string{"light.kitchen", "light.none"})
	if !up || len(states) != 1 || states["light.kitchen"] != "on" {
		t.Fatalf("loaded: %v %v", up, states)
	}
	h.fake.Emit("state_changed", stateChanged("light.kitchen", state("light.kitchen", "off", nil)))
	waitClosed(t, "the change", next)
	eventually(t, "the new state", func() bool {
		_, s, _ := h.home.Watch([]string{"light.kitchen"})
		return s["light.kitchen"] == "off"
	})
	_, _, next = h.home.Watch(nil)
	h.fake.Restart(time.Hour)
	waitClosed(t, "the disconnect", next)
	eventually(t, "down", func() bool { up, s, _ := h.home.Watch([]string{"light.kitchen"}); return !up && s == nil })
}

func waitClosed(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: not woken", what)
	}
}
