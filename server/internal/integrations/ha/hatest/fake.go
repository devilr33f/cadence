// Package hatest is a fake Home Assistant WebSocket API over TLS, for tests:
// it checks the token, answers scripted commands, emits events to
// subscriptions, records service calls, and has hooks that fail or hold
// commands, drop or restart connections and redirect the upgrade.
package hatest

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Leak is the text the fake puts in every error message and auth_invalid, so
// tests can assert it never surfaces.
const Leak = "ha-upstream-text-that-must-never-surface"

const (
	version   = "2026.11.0"
	readLimit = 1 << 20
)

// Call is one call_service the fake received. Target and ServiceData are nil
// when the command left them out.
type Call struct {
	Domain, Service string
	ServiceData     map[string]any
	Target          map[string]any
	ReturnResponse  bool
}

type command struct {
	ID             int64          `json:"id"`
	Type           string         `json:"type"`
	EventType      string         `json:"event_type"`
	Domain         string         `json:"domain"`
	Service        string         `json:"service"`
	ServiceData    map[string]any `json:"service_data"`
	Target         map[string]any `json:"target"`
	ReturnResponse bool           `json:"return_response"`
	EntityIDs      []string       `json:"entity_ids"`
}

func (cmd command) call() Call {
	return Call{
		Domain: cmd.Domain, Service: cmd.Service, ServiceData: cmd.ServiceData, Target: cmd.Target,
		ReturnResponse: cmd.ReturnResponse,
	}
}

// getEntries answers with the scripted entries of the ids asked for, null for
// the rest, as home assistant does
const getEntries = "config/entity_registry/get_entries"

type conn struct {
	ws  *websocket.Conn
	ctx context.Context
	wmu sync.Mutex
	// subs maps a subscription id to its event type; guarded by Server.mu
	subs   map[int64]string
	lastID int64
}

type held struct {
	c   *conn
	cmd command
}

// Server is the fake. Its zero value is not usable; call New or NewHandler.
type Server struct {
	token string
	srv   *httptest.Server

	mu        sync.Mutex
	results   map[string]json.RawMessage
	fails     map[string]string
	hold      map[string]bool
	queue     []held
	calls     []Call
	onCall    func(Call)
	received  map[string]int
	conns     map[*conn]bool
	auths     int
	upgrades  int
	lastPath  string
	redirect  string
	downUntil time.Time
}

// New starts a fake that accepts token. Close it when done.
func New(token string) *Server {
	s := NewHandler(token)
	s.srv = httptest.NewTLSServer(s)
	return s
}

// NewHandler is a fake that accepts token and listens nowhere: the caller
// serves it on its own server, as the e2e's fake home assistant does. URL, CA
// and Client need New.
func NewHandler(token string) *Server {
	return &Server{
		token:    token,
		results:  map[string]json.RawMessage{},
		fails:    map[string]string{},
		hold:     map[string]bool{},
		received: map[string]int{},
		conns:    map[*conn]bool{},
	}
}

// Close drops every connection and stops the server New started.
func (s *Server) Close() {
	s.Drop()
	if s.srv != nil {
		s.srv.Close()
	}
}

// URL is the base URL to configure (https, no path).
func (s *Server) URL() string { return s.srv.URL }

// CA trusts the fake's certificate.
func (s *Server) CA() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(s.srv.Certificate())
	return p
}

// Client is an httpx client (verified TLS, no redirects) that trusts the fake.
func (s *Server) Client() *http.Client { return httpx.Client(s.CA()) }

// SetResult scripts the success result of a command type, such as get_states
// or config/area_registry/list. A map scripted for
// config/entity_registry/get_entries is answered per request with only the
// ids it asked for (null for an unknown one). It panics on a value JSON
// cannot encode.
func (s *Server) SetResult(typ string, result any) {
	b, err := json.Marshal(result)
	if err != nil {
		panic("hatest: result not encodable")
	}
	s.mu.Lock()
	s.results[typ] = b
	s.mu.Unlock()
}

// Fail makes every command of typ fail with code (and Leak as the message);
// an empty code clears it.
func (s *Server) Fail(typ, code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if code == "" {
		delete(s.fails, typ)
		return
	}
	s.fails[typ] = code
}

// Hold queues commands of typ (ping included) unanswered until Release.
func (s *Server) Hold(typ string) {
	s.mu.Lock()
	s.hold[typ] = true
	s.mu.Unlock()
}

// Held is how many commands wait for Release.
func (s *Server) Held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// Release stops holding and answers every held command in reverse order of
// arrival, so results reach the client out of order.
func (s *Server) Release() {
	s.mu.Lock()
	q := s.queue
	s.queue = nil
	clear(s.hold)
	s.mu.Unlock()
	for _, h := range slices.Backward(q) {
		s.answer(h.c, h.cmd)
	}
}

// Emit sends an event of eventType to every subscription for it.
func (s *Server) Emit(eventType string, data any) {
	type target struct {
		c  *conn
		id int64
	}
	var to []target
	s.mu.Lock()
	for c := range s.conns {
		for id, et := range c.subs {
			if et == eventType {
				to = append(to, target{c, id})
			}
		}
	}
	s.mu.Unlock()
	for _, t := range to {
		_ = t.c.send(map[string]any{"id": t.id, "type": "event", "event": map[string]any{
			"event_type": eventType, "data": data, "origin": "LOCAL", "time_fired": time.Now().UTC().Format(time.RFC3339Nano),
		}})
	}
}

// Drop closes every connection without a close handshake.
func (s *Server) Drop() {
	s.mu.Lock()
	cs := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	for _, c := range cs {
		_ = c.ws.CloseNow()
	}
}

// Restart drops every connection and refuses upgrades (503) for d.
func (s *Server) Restart(d time.Duration) {
	s.mu.Lock()
	s.downUntil = time.Now().Add(d)
	s.mu.Unlock()
	s.Drop()
}

// Redirect answers upgrades with a 307 to url; empty stops it.
func (s *Server) Redirect(url string) {
	s.mu.Lock()
	s.redirect = url
	s.mu.Unlock()
}

// OnCall makes f run after each call_service the fake answered with success,
// on the goroutine that answered it: the connection's, before it reads its
// next command, or Release's for a held one. Where Home Assistant would
// change a state, f can emit it. It replaces an earlier f; nil stops it.
func (s *Server) OnCall(f func(Call)) {
	s.mu.Lock()
	s.onCall = f
	s.mu.Unlock()
}

// Calls are the service calls received, in order, held and failed ones
// included: a call is recorded on receipt, before any answer.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// Received counts the commands of typ received on any connection, held or
// failed ones included.
func (s *Server) Received(typ string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.received[typ]
}

// Auths counts auth messages received, valid or not.
func (s *Server) Auths() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auths
}

// Upgrades counts accepted WebSocket upgrades.
func (s *Server) Upgrades() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upgrades
}

// LastPath is the path of the latest accepted upgrade.
func (s *Server) LastPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPath
}

// Conns counts authenticated connections still open.
func (s *Server) Conns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Subscriptions counts subscriptions to eventType on open connections.
func (s *Server) Subscriptions(eventType string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for c := range s.conns {
		for _, et := range c.subs {
			if et == eventType {
				n++
			}
		}
	}
	return n
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/api/websocket") {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	redirect, down := s.redirect, time.Now().Before(s.downUntil)
	s.mu.Unlock()
	if redirect != "" {
		http.Redirect(w, r, redirect, http.StatusTemporaryRedirect)
		return
	}
	if down {
		http.Error(w, "restarting", http.StatusServiceUnavailable)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(readLimit)
	s.mu.Lock()
	s.upgrades++
	s.lastPath = r.URL.Path
	s.mu.Unlock()
	c := &conn{ws: ws, ctx: r.Context(), subs: map[int64]string{}}
	defer func() { _ = ws.CloseNow() }()
	if !s.authenticate(c) {
		return
	}
	s.mu.Lock()
	s.conns[c] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	for {
		typ, b, err := ws.Read(c.ctx)
		if err != nil {
			return
		}
		var cmd command
		if typ != websocket.MessageText || json.Unmarshal(b, &cmd) != nil {
			return
		}
		s.handle(c, cmd)
	}
}

func (s *Server) authenticate(c *conn) bool {
	if c.send(map[string]any{"type": "auth_required", "ha_version": version}) != nil {
		return false
	}
	_, b, err := c.ws.Read(c.ctx)
	if err != nil {
		return false
	}
	var auth struct {
		Type  string `json:"type"`
		Token string `json:"access_token"`
	}
	_ = json.Unmarshal(b, &auth)
	s.mu.Lock()
	s.auths++
	s.mu.Unlock()
	if auth.Type != "auth" || auth.Token != s.token {
		_ = c.send(map[string]any{"type": "auth_invalid", "message": Leak})
		return false
	}
	return c.send(map[string]any{"type": "auth_ok", "ha_version": version}) == nil
}

func (s *Server) handle(c *conn, cmd command) {
	// home assistant refuses an id that does not increase
	if cmd.ID <= c.lastID {
		_ = c.send(errorResult(cmd.ID, "id_reuse"))
		return
	}
	c.lastID = cmd.ID
	s.mu.Lock()
	s.received[cmd.Type]++
	if cmd.Type == "call_service" {
		s.calls = append(s.calls, cmd.call())
	}
	if s.hold[cmd.Type] {
		s.queue = append(s.queue, held{c, cmd})
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.answer(c, cmd)
}

func (s *Server) answer(c *conn, cmd command) {
	s.mu.Lock()
	code, failing := s.fails[cmd.Type]
	result, scripted := s.results[cmd.Type]
	onCall := s.onCall
	s.mu.Unlock()
	switch {
	case failing:
		_ = c.send(errorResult(cmd.ID, code))
	case cmd.Type == "ping":
		_ = c.send(map[string]any{"id": cmd.ID, "type": "pong"})
	case cmd.Type == "subscribe_events":
		s.mu.Lock()
		c.subs[cmd.ID] = cmd.EventType
		s.mu.Unlock()
		_ = c.send(okResult(cmd.ID, nil))
	case cmd.Type == "call_service":
		_ = c.send(okResult(cmd.ID, map[string]any{"context": map[string]any{"id": "fake-context"}, "response": nil}))
		if onCall != nil {
			onCall(cmd.call())
		}
	case cmd.Type == getEntries && scripted:
		_ = c.send(okResult(cmd.ID, entriesFor(result, cmd.EntityIDs)))
	case scripted:
		_ = c.send(okResult(cmd.ID, result))
	default:
		_ = c.send(errorResult(cmd.ID, "unknown_command"))
	}
}

// entriesFor picks the asked ids out of a scripted map; anything else is
// answered as scripted.
func entriesFor(scripted json.RawMessage, ids []string) any {
	var all map[string]json.RawMessage
	if json.Unmarshal(scripted, &all) != nil {
		return scripted
	}
	out := make(map[string]json.RawMessage, len(ids))
	for _, id := range ids {
		if e, ok := all[id]; ok {
			out[id] = e
		} else {
			out[id] = json.RawMessage("null")
		}
	}
	return out
}

func okResult(id int64, result any) map[string]any {
	return map[string]any{"id": id, "type": "result", "success": true, "result": result}
}

func errorResult(id int64, code string) map[string]any {
	return map[string]any{"id": id, "type": "result", "success": false, "error": map[string]any{"code": code, "message": Leak}}
}

func (c *conn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.Write(c.ctx, websocket.MessageText, b)
}
