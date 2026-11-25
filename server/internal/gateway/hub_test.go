package gateway

import (
	"errors"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func delta() *cadencev2.Envelope {
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_StateDelta{StateDelta: &cadencev2.StateDelta{Capability: "volume"}}}
}

func TestSendBackpressure(t *testing.T) {
	h := NewHub()
	// no writer runs, so nothing drains the queue
	h.attach(newConn("dev-1", nil, 0, 0), func() {})
	for i := range queueSize {
		if err := h.Send("dev-1", delta()); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if err := h.Send("dev-1", delta()); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("send 257: %v", err)
	}
	if !h.Online("dev-1") {
		t.Fatal("backpressure took the device offline")
	}
}

// a removed device leaves no lifecycle lock behind, linked or not, and a
// later link of it is refused before its hello
func TestRemoveDropsTheDevicesState(t *testing.T) {
	h := NewHub()
	c := newConn("dev-1", nil, 0, 0)
	h.attach(c, func() {})
	h.Remove("dev-1")
	if !c.closing() || c.code != 1008 {
		t.Fatalf("closing %v code %d", c.closing(), c.code)
	}
	h.detach(c, func() {})
	h.Remove("dev-2")
	if n := len(h.life); n != 0 {
		t.Fatalf("%d lifecycle locks left", n)
	}
	late := newConn("dev-1", nil, 0, 0)
	if h.attach(late, func() { t.Fatal("hello of a removed device") }) || late.code != 1008 {
		t.Fatalf("late link attached, code %d", late.code)
	}
	if n := len(h.life); n != 0 || h.Online("dev-1") {
		t.Fatalf("%d lifecycle locks left, online %v", n, h.Online("dev-1"))
	}
}

func TestSendOffline(t *testing.T) {
	h := NewHub()
	if err := h.Send("dev-1", delta()); !errors.Is(err, ErrOffline) {
		t.Fatalf("unknown device: %v", err)
	}
	c := newConn("dev-1", nil, 0, 0)
	h.attach(c, func() {})
	c.closeWith(1001, "")
	if err := h.Send("dev-1", delta()); !errors.Is(err, ErrOffline) {
		t.Fatalf("closing conn: %v", err)
	}
	if h.Online("dev-1") {
		t.Fatal("closing conn counts as online")
	}
	h.detach(c, func() {})
	if err := h.Send("dev-1", delta()); !errors.Is(err, ErrOffline) {
		t.Fatalf("detached conn: %v", err)
	}
}
