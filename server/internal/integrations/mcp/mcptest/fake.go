// Package mcptest is a fake MCP server for tests: the go-sdk's own server
// behind httptest.NewTLSServer, in the modern era (stateless) or the legacy
// era (stateful with session ids, or stateless), with knobs that fail,
// redirect, oversize, mutate, cache and drop, and records of what a client
// sent.
package mcptest

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Leak is the text the fake puts in every error body, isError result and
// JSON-RPC error, so tests can assert it never surfaces.
const Leak = "mcp-upstream-text-that-must-never-surface"

// Path is the endpoint's path; URL ends with it.
const Path = "/mcp"

// HugeChars is the length of the huge tool's text, well under the body cap.
const HugeChars = 100_000

// metaCapabilities is where a modern request carries the client's capabilities.
const metaCapabilities = "io.modelcontextprotocol/clientCapabilities"

type Mode int

const (
	// Modern speaks 2026-07-28: server/discover, no session
	Modern Mode = iota
	// Legacy is stateful: initialize, then Mcp-Session-Id on every request
	Legacy
	// LegacyStateless answers initialize and keeps no session
	LegacyStateless
)

type Options struct {
	Mode Mode
	// PageSize splits tools/list into pages; zero is the SDK's default
	PageSize int
	// JSONResponse answers with application/json instead of an SSE stream
	JSONResponse bool
	// ListTTL lets a modern client cache each tools/list page this long
	ListTTL time.Duration
}

// Request is one HTTP request the fake received.
type Request struct {
	// Method is the HTTP method; RPC the JSON-RPC method of a POST
	Method, RPC   string
	Authorization string
}

// Server is the fake. Built-in tools: echo (returns "ok"), slow (answers
// once released, or when the call's context or the server ends), fail
// (isError), huge (HugeChars of text) and image (only an image).
type Server struct {
	srv  *httptest.Server
	mcp  *sdk.Server
	h    http.Handler
	stop chan struct{}
	once sync.Once

	mu       sync.Mutex
	requests []Request
	caps     []string
	calls    map[string]int
	args     map[string][]string
	release  chan struct{}
	status   int
	next     int
	redirect string
	oversize string
	repeat   bool
	raw      string
	// ids seen in requests, and those answered as forgotten
	seen, forgotten map[string]bool
	// hold, while set, keeps every request waiting
	hold chan struct{}
}

// New starts a fake. Close it when done.
func New(o Options) *Server {
	s := NewHandler(o)
	s.srv = httptest.NewUnstartedServer(s)
	// a client refusing the certificate is a test case, not news
	s.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.srv.StartTLS()
	return s
}

// NewHandler is a fake that listens nowhere: the caller serves it on its own
// server at Path, as the e2e's fake MCP server does. URL, CA, Certificate
// and Client need New.
func NewHandler(o Options) *Server {
	so := &sdk.ServerOptions{PageSize: o.PageSize}
	if o.Mode == LegacyStateless {
		so.SupportedProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}
	}
	if o.ListTTL > 0 {
		so.SetCacheable = func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) { c.TTLMs = int(o.ListTTL.Milliseconds()) }
	}
	s := &Server{
		mcp:       sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "1"}, so),
		stop:      make(chan struct{}),
		calls:     map[string]int{},
		args:      map[string][]string{},
		release:   make(chan struct{}),
		seen:      map[string]bool{},
		forgotten: map[string]bool{},
	}
	s.h = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.mcp },
		&sdk.StreamableHTTPOptions{Stateless: o.Mode != Legacy, JSONResponse: o.JSONResponse})
	s.add("echo", "Echoes.", func(context.Context) (*sdk.CallToolResult, error) { return text("ok"), nil })
	s.add("slow", "Waits.", func(ctx context.Context) (*sdk.CallToolResult, error) {
		s.mu.Lock()
		release := s.release
		s.mu.Unlock()
		select {
		case <-release:
		case <-ctx.Done():
		case <-s.stop:
		}
		return text("done"), nil
	})
	s.add("fail", "Fails.", func(context.Context) (*sdk.CallToolResult, error) {
		r := text(Leak)
		r.IsError = true
		return r, nil
	})
	s.add("huge", "Talks.", func(context.Context) (*sdk.CallToolResult, error) {
		return text(strings.Repeat("h", HugeChars)), nil
	})
	s.add("image", "Draws.", func(context.Context) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.ImageContent{Data: []byte{0x89, 'P', 'N', 'G'}, MIMEType: "image/png"}}}, nil
	})
	return s
}

func text(t string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: t}}}
}

func (s *Server) add(name, desc string, fn func(context.Context) (*sdk.CallToolResult, error)) {
	s.mcp.AddTool(&sdk.Tool{
		Name: name, Description: desc,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
	}, s.handler(name, func(ctx context.Context, _ json.RawMessage) (*sdk.CallToolResult, error) { return fn(ctx) }))
}

// handler counts a tool's runs and keeps the arguments each came with.
func (s *Server) handler(name string, fn func(context.Context, json.RawMessage) (*sdk.CallToolResult, error)) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		s.mu.Lock()
		s.calls[name]++
		s.args[name] = append(s.args[name], string(req.Params.Arguments))
		s.mu.Unlock()
		return fn(ctx, req.Params.Arguments)
	}
}

// Add adds an echo tool, or replaces the one of that name: a description
// changed between two listings.
func (s *Server) Add(name, description string) {
	s.add(name, description, func(context.Context) (*sdk.CallToolResult, error) { return text("ok"), nil })
}

// AddTool adds a tool as t describes it, or replaces the one of its name: a
// schema or annotations changed between two listings. Its calls answer "ok".
// The SDK's server takes only an object schema of type object; ListRaw
// lists anything else.
func (s *Server) AddTool(t *sdk.Tool) { s.AddResult(t, text("ok")) }

// AddResult adds a tool as t describes it, or replaces the one of its name,
// whose every call answers res: structured, mixed or empty content.
func (s *Server) AddResult(t *sdk.Tool, res *sdk.CallToolResult) {
	s.AddFunc(t, func(json.RawMessage) *sdk.CallToolResult { return res })
}

// AddFunc adds a tool as t describes it, or replaces the one of its name,
// whose every call answers what fn makes of the call's raw arguments.
func (s *Server) AddFunc(t *sdk.Tool, fn func(args json.RawMessage) *sdk.CallToolResult) {
	s.mcp.AddTool(t, s.handler(t.Name, func(_ context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
		return fn(args), nil
	}))
}

// ListRaw answers every tools/list with tools, a raw JSON array, in one
// page; empty lists the server's tools again.
func (s *Server) ListRaw(tools string) {
	s.mu.Lock()
	s.raw = tools
	s.mu.Unlock()
}

// AddRPCError adds a tool whose every call is refused with a JSON-RPC error
// of code and Leak as its message.
func (s *Server) AddRPCError(name string, code int64) {
	s.add(name, "Refuses.", func(context.Context) (*sdk.CallToolResult, error) {
		return nil, &jsonrpc.Error{Code: code, Message: Leak}
	})
}

// Remove drops tools by name.
func (s *Server) Remove(names ...string) { s.mcp.RemoveTools(names...) }

// Release answers the slow calls running now.
func (s *Server) Release() {
	s.mu.Lock()
	close(s.release)
	s.release = make(chan struct{})
	s.mu.Unlock()
}

// Close ends the slow calls, drops every session and stops the server New
// started.
func (s *Server) Close() {
	s.once.Do(func() { close(s.stop) })
	s.DropSessions()
	if s.srv != nil {
		s.srv.Close()
	}
}

// URL is the endpoint to configure.
func (s *Server) URL() string { return s.srv.URL + Path }

// CA trusts the fake's certificate.
func (s *Server) CA() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(s.srv.Certificate())
	return p
}

// Certificate is the fake's own certificate.
func (s *Server) Certificate() *x509.Certificate { return s.srv.Certificate() }

// Client is an httpx client (verified TLS, no redirects) that trusts the fake.
func (s *Server) Client() *http.Client { return httpx.Client(s.CA()) }

// DropSessions ends every session the server holds: a legacy client's next
// request gets 404, as after a server restart.
func (s *Server) DropSessions() {
	for ss := range s.mcp.Sessions() {
		_ = ss.Close()
	}
}

// Fail answers every request with status and Leak; zero answers again.
func (s *Server) Fail(status int) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

// FailNext answers the next request alone with status and Leak.
func (s *Server) FailNext(status int) {
	s.mu.Lock()
	s.next = status
	s.mu.Unlock()
}

// RedirectTo answers every request with a 302 to url; empty stops.
func (s *Server) RedirectTo(url string) {
	s.mu.Lock()
	s.redirect = url
	s.mu.Unlock()
}

// OversizeOn answers requests of a JSON-RPC method with a JSON body over
// 1 MiB; empty stops.
func (s *Server) OversizeOn(method string) {
	s.mu.Lock()
	s.oversize = method
	s.mu.Unlock()
}

// RepeatCursor answers every tools/list with the same next cursor.
func (s *Server) RepeatCursor(on bool) {
	s.mu.Lock()
	s.repeat = on
	s.mu.Unlock()
}

// ForgetSessions ends every session and answers each later request in one
// of them with a 404 and a JSON-RPC error body, as older MCP SDKs do, where
// the SDK finds no session-missing error.
func (s *Server) ForgetSessions() {
	s.mu.Lock()
	for id := range s.seen {
		s.forgotten[id] = true
	}
	s.mu.Unlock()
	s.DropSessions()
}

// Hold keeps every request waiting, once on, until it is turned off, the
// request's context ends or the server closes.
func (s *Server) Hold(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case on && s.hold == nil:
		s.hold = make(chan struct{})
	case !on && s.hold != nil:
		close(s.hold)
		s.hold = nil
	}
}

// Requests is every request received, on any path, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Hits counts every request, on any path.
func (s *Server) Hits() int { return len(s.Requests()) }

// Count counts the POSTs of a JSON-RPC method, answered or not.
func (s *Server) Count(rpc string) int {
	n := 0
	for _, r := range s.Requests() {
		if r.RPC == rpc {
			n++
		}
	}
	return n
}

// Authorizations is the Authorization header of every request, in order;
// empty for a request without one.
func (s *Server) Authorizations() []string {
	var out []string
	for _, r := range s.Requests() {
		out = append(out, r.Authorization)
	}
	return out
}

// Capabilities is every client capabilities object received, as JSON: from
// initialize's params and from a modern request's _meta.
func (s *Server) Capabilities() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.caps...)
}

// Calls counts the runs of a tool.
func (s *Server) Calls(tool string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[tool]
}

// Args is the raw arguments of each run of a tool, in order.
func (s *Server) Args(tool string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.args[tool]...)
}

// SessionIDs is every Mcp-Session-Id a client sent, sorted.
func (s *Server) SessionIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Sorted(maps.Keys(s.seen))
}

type rpc struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Capabilities json.RawMessage            `json:"capabilities"`
		Meta         map[string]json.RawMessage `json:"_meta"`
	} `json:"params"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var m rpc
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, Leak, http.StatusBadRequest)
			return
		}
		_ = json.Unmarshal(body, &m)
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, RPC: m.Method, Authorization: r.Header.Get("Authorization")})
	if m.Params.Capabilities != nil {
		s.caps = append(s.caps, string(m.Params.Capabilities))
	}
	if c, ok := m.Params.Meta[metaCapabilities]; ok {
		s.caps = append(s.caps, string(c))
	}
	id := r.Header.Get("Mcp-Session-Id")
	if id != "" {
		s.seen[id] = true
	}
	status, redirect, oversize, repeat, hold, raw := s.status, s.redirect, s.oversize, s.repeat, s.hold, s.raw
	if s.next != 0 {
		status, s.next = s.next, 0
	}
	forgotten := s.forgotten[id]
	s.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		case <-s.stop:
			return
		}
	}
	switch {
	case r.URL.Path != Path:
		http.Error(w, Leak, http.StatusNotFound)
	case status != 0:
		http.Error(w, Leak, status)
	case forgotten:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":"server-error","error":{"code":-32600,"message":"%s"}}`, Leak)
	case redirect != "":
		http.Redirect(w, r, redirect, http.StatusFound)
	case m.Method != "" && m.Method == oversize:
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[],"pad":"%s"}}`, m.ID, strings.Repeat(Leak, (1<<20)/len(Leak)+1))
	case raw != "" && m.Method == "tools/list":
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":%s}}`, m.ID, raw)
	case repeat && m.Method == "tools/list":
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"r","inputSchema":{"type":"object"}}],"nextCursor":"again"}}`, m.ID)
	default:
		s.h.ServeHTTP(w, r)
	}
}
