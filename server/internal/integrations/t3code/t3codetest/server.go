// Package t3codetest is a fake T3 Code server for tests and the e2e: the
// orchestration protocol 2 subset Cadence's t3code client speaks, over the
// same Effect RPC JSON framing on /ws (Request, Chunk with a strict Ack per
// chunk, Exit, Ping/Pong, Interrupt, Eof), the HTTP shell snapshot and
// websocket tickets. Threads are scripted by the first word of a message
// (see script.go); every command it takes is recorded. It logs methods, ids
// and codes only.
package t3codetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Config struct {
	// Token is the bearer every request must carry
	Token string
	// TicketOnly refuses the bearer on the upgrade (401), so a client must
	// buy a websocket ticket first
	TicketOnly bool
	// Step is the delay between a script's steps; zero is 20 ms
	Step time.Duration
	// Now nil is time.Now
	Now func() time.Time
	// Log nil discards
	Log *slog.Logger
	// OnCommand, when set, hears each command taken, after it applied (the
	// e2e's record of them)
	OnCommand func(Command)
}

// Subscribe is one stream request the fake took.
type Subscribe struct {
	Method, ThreadID string
	// AfterSequence nil when the request had none
	AfterSequence *int64
}

type Server struct {
	cfg Config
	st  *state

	mu         sync.Mutex
	tickets    map[string]bool
	commands   []Command
	subscribes []Subscribe
	acks       int
	pings      int
	conns      map[*websocket.Conn]context.CancelFunc
	byCommand  map[string]int64
}

func New(cfg Config) *Server {
	if cfg.Step <= 0 {
		cfg.Step = 20 * time.Millisecond
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Server{cfg: cfg, st: newState(cfg.Now), tickets: map[string]bool{}, conns: map[*websocket.Conn]context.CancelFunc{}, byCommand: map[string]int64{}}
}

// Commands is every command taken, in order.
func (s *Server) Commands() []Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Command(nil), s.commands...)
}

// Subscribes is every stream request taken, in order.
func (s *Server) Subscribes() []Subscribe {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Subscribe(nil), s.subscribes...)
}

// Acks and Pings count the frames of that tag received.
func (s *Server) Acks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acks
}

func (s *Server) Pings() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pings
}

// Drop closes every open WebSocket, as a server restart would.
func (s *Server) Drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c, cancel := range s.conns {
		cancel()
		_ = c.CloseNow()
	}
}

// Start begins a run of text on a thread, as a message from T3's own UI.
func (s *Server) Start(threadID, text string) bool {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	t := s.st.threads[threadID]
	if t == nil || t.activeID != "" {
		return false
	}
	s.st.start(t, text, s.cfg.Step)
	return true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/ws" && r.Method == http.MethodGet:
		s.serveWS(w, r)
	case r.URL.Path == "/api/auth/websocket-ticket" && r.Method == http.MethodPost:
		if !s.bearer(r) {
			http.Error(w, `{"code":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var b [16]byte
		_, _ = rand.Read(b[:])
		ticket := hex.EncodeToString(b[:])
		s.mu.Lock()
		s.tickets[ticket] = true
		s.mu.Unlock()
		writeJSON(w, obj{"ticket": ticket, "expiresAt": s.cfg.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
	case r.URL.Path == "/api/orchestration/shell" && r.Method == http.MethodGet:
		if !s.bearer(r) {
			http.Error(w, `{"code":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if r.Header.Get("x-t3-orchestration-protocol") != "2" {
			http.Error(w, `{"code":"orchestration_protocol_incompatible"}`, http.StatusUpgradeRequired)
			return
		}
		s.st.mu.Lock()
		snap := s.st.shellSnapshot()
		s.st.mu.Unlock()
		writeJSON(w, snap)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) bearer(r *http.Request) bool {
	return s.cfg.Token != "" && r.Header.Get("Authorization") == "Bearer "+s.cfg.Token
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("orchestrationProtocol") != "2" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"code":"orchestration_protocol_incompatible","orchestrationProtocolVersion":2}`))
		return
	}
	ok := false
	if ticket := r.URL.Query().Get("wsTicket"); ticket != "" {
		s.mu.Lock()
		ok = s.tickets[ticket]
		delete(s.tickets, ticket)
		s.mu.Unlock()
	} else if !s.cfg.TicketOnly {
		ok = s.bearer(r)
	}
	if !ok {
		http.Error(w, `{"code":"environment_auth_invalid"}`, http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(4 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	s.mu.Lock()
	s.conns[c] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		cancel()
		_ = c.CloseNow()
	}()
	s.cfg.Log.Info("ws_open")
	newPeer(ctx, s, c).run()
	s.cfg.Log.Info("ws_closed")
}
