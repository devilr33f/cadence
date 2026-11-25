// Package gateway serves the device link at /v2/device and routes envelopes to connected devices.
package gateway

import (
	"errors"
	"sync"
	"time"

	"github.com/gofiber/contrib/v3/websocket"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

var (
	ErrOffline      = errors.New("gateway: device offline")
	ErrBackpressure = errors.New("gateway: send queue full")
)

const closeAllWait = 2 * time.Second

// Hub holds the live connection of each device.
type Hub struct {
	mu    sync.Mutex
	conns map[string]*conn
	// per device, so its callbacks never interleave
	life map[string]*sync.Mutex
	// deleted devices, whose links are refused: device ids are never reused,
	// so it grows by a delete alone
	removed map[string]struct{}
	// set by Close: later links are refused with this code
	closed bool
	code   int
	reason string
}

func NewHub() *Hub {
	return &Hub{conns: map[string]*conn{}, life: map[string]*sync.Mutex{}, removed: map[string]struct{}{}}
}

// Remove ends the deleted device's link with 1008 and refuses its later ones:
// a link its token opened before the delete must not attach after it. The
// closing link still reports the device gone (OnClose); its lifecycle lock
// goes once nothing can use it.
func (h *Hub) Remove(deviceID string) {
	h.mu.Lock()
	h.removed[deviceID] = struct{}{}
	c := h.conns[deviceID]
	if c == nil {
		delete(h.life, deviceID)
	}
	h.mu.Unlock()
	if c != nil {
		c.closeWith(websocket.ClosePolicyViolation, "")
	}
}

// Send queues env for whichever connection the device has now, without
// blocking. Frames that belong to one link or session go through that Conn
// instead: its Send fails with ErrOffline once the link was replaced.
func (h *Hub) Send(deviceID string, env *cadencev2.Envelope) error {
	c := h.get(deviceID)
	if c == nil {
		return ErrOffline
	}
	return c.Send(env)
}

func (h *Hub) Online(deviceID string) bool {
	c := h.get(deviceID)
	return c != nil && !c.closing()
}

// Close ends every link with code and reason (at most 123 bytes, to fit a
// close frame) and refuses later ones. It waits up to closeAllWait for the
// links' readers to finish.
func (h *Hub) Close(code int, reason string) {
	h.mu.Lock()
	h.closed, h.code, h.reason = true, code, reason
	conns := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.closeWith(code, reason)
	}
	deadline := time.After(closeAllWait)
	for _, c := range conns {
		select {
		case <-c.ended:
		case <-deadline:
			return
		}
	}
}

func (h *Hub) get(id string) *conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[id]
}

func (h *Hub) current(c *conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[c.id] == c
}

func (h *Hub) lifecycle(id string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.life[id]
	if l == nil {
		l = &sync.Mutex{}
		h.life[id] = l
	}
	return l
}

// attach makes c the device's connection, closes the one it replaces with
// 1001 and runs hello. A closed hub, or a removed device, closes c instead and
// reports false.
func (h *Hub) attach(c *conn, hello func()) bool {
	l := h.lifecycle(c.id)
	l.Lock()
	defer l.Unlock()
	h.mu.Lock()
	if h.closed {
		code, reason := h.code, h.reason
		h.mu.Unlock()
		c.closeWith(code, reason)
		return false
	}
	if _, gone := h.removed[c.id]; gone {
		// a refused link runs no callback, so nothing needs the lock it made
		delete(h.life, c.id)
		h.mu.Unlock()
		c.closeWith(websocket.ClosePolicyViolation, "")
		return false
	}
	old := h.conns[c.id]
	h.conns[c.id] = c
	h.mu.Unlock()
	if old != nil {
		old.closeWith(websocket.CloseGoingAway, "")
	}
	hello()
	return true
}

// deliver runs fn only while c is the device's open connection, and never
// beside another callback of the device.
func (h *Hub) deliver(c *conn, fn func()) {
	l := h.lifecycle(c.id)
	l.Lock()
	defer l.Unlock()
	if h.current(c) && !c.closing() {
		fn()
	}
}

// detach runs closed only while c is still the device's connection, so a
// replaced connection never reports its device gone.
func (h *Hub) detach(c *conn, closed func()) {
	l := h.lifecycle(c.id)
	l.Lock()
	defer l.Unlock()
	h.mu.Lock()
	current := h.conns[c.id] == c
	if current {
		delete(h.conns, c.id)
	}
	if _, gone := h.removed[c.id]; gone {
		// no link of the device attaches again, so no callback waits on it
		delete(h.life, c.id)
	}
	h.mu.Unlock()
	if current {
		closed()
	}
}
