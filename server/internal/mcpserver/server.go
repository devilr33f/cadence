package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/assistant"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/favorites"
	"git.shino.boo/devilreef/cadence/server/internal/history"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
	"git.shino.boo/devilreef/cadence/server/internal/voice"
)

// Deps are the MCP endpoint's collaborators: functional seams over the same
// services the admin api and the integrations use, so a rig wires fakes
// without a database.
type Deps struct {
	Log     *slog.Logger
	Version string
	// Tools is the agent toolbox's static snapshot (agent.Registry.Static);
	// the sources' tools are never served, so another MCP server's tools
	// cannot come back through this one
	Tools func() []agent.Tool
	// Target resolves a device argument (name, room, kind) to the device a
	// call acts as; nil: ids only
	Target tools.TargetFunc
	// Settings reads a device's effective settings
	Settings func(ctx context.Context, deviceID string) (settings.Values, error)

	// the household's devices and their state
	List     func(ctx context.Context) ([]enroll.Listed, error)
	Online   func(deviceID string) bool
	Detail   func(ctx context.Context, deviceID string) (devices.Detail, error)
	Vitals   func(deviceID string) (telemetry.Vitals, bool)
	Send     func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
	Session  func(deviceID string) (media.MediaSession, bool)
	Queue    func(deviceID string) music.View
	History  func(ctx context.Context, deviceID, before string, limit int) (history.HistoryPage, error)
	Favorite func(ctx context.Context, query string, limit int) ([]favorites.Entry, error)
	Announce func(ctx context.Context, deviceID string, a voice.Announcement) (string, error)
	Ask      func(ctx context.Context, r assistant.Request) (agent.Result, string, error)

	// media links
	// Track is the song an id of a device's recent music results names
	Track func(deviceID, id string) (providers.Track, bool)
	// Music builds the music provider instance a track names
	Music func(ctx context.Context, name string) (providers.Music, error)
	// Player resolves the YouTube audio player (the musicPlayer instance the
	// settings name); nil: no such resolution
	Player func(ctx context.Context) (providers.Player, error)
	// Stream relays a resolved stream through the server's relay at the
	// public origin and answers its url; nil: the upstream url itself
	Stream func(deviceID string, upstream providers.Stream) (string, error)
	// RelayExpiry says when the relay drops a stream (for a link's
	// expiresAt); nil: unknown
	RelayExpiry func() time.Time
	// ResolveCatalog resolves a catalog ref (and episode) to its playable
	// view, the household's own
	ResolveCatalog func(ctx context.Context, ref, episode string) (catalog.PlayableView, error)
	Now            func() time.Time
}

// Server is the MCP endpoint: the per-request toolset, one server built a
// request at a time under the calling token's grants.
type Server struct {
	d     Deps
	log   *slog.Logger
	cache *sdk.SchemaCache
	// toolNames holds the whole toolset's names in serve order, for the
	// tokens' validation and the admin list, read once at start
	toolNames []string
}

// New assembles the endpoint; a nil Log discards.
func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.RelayExpiry == nil {
		d.RelayExpiry = func() time.Time { return time.Time{} }
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	s := &Server{d: d, cache: sdk.NewSchemaCache()}
	s.toolNames = names(s.definitions(nil))
	return s
}

// logTool notes a call by its tool, code and milliseconds only (the privacy
// contract: no argument, no result, no title in a log).
func (s *Server) logTool(name string, start time.Time, code string) {
	s.d.Log.Info("mcp_tool", "tool", name, "code", code, "ms", time.Since(start).Milliseconds())
}

// now is the server's clock.
func (s *Server) now() time.Time { return s.d.Now() }

// Names are every tool's name, in serve order: what a token may name.
func (s *Server) Names() []string { return s.toolNames }

// definition is one served tool: its MCP shape and its call.
type definition struct {
	name, description string
	schema            any
	annotations       *sdk.ToolAnnotations
	call              func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error)
}

// definitions is the whole toolset, the service-backed tools first, then the
// agent's; grants nil serves them all (the admin list, and the tool list of
// a token that names none).
func (s *Server) definitions(grants func(string) bool) []definition {
	var out []definition
	take := func(d definition) {
		if grants == nil || grants(d.name) {
			out = append(out, d)
		}
	}
	s.deviceTools(take)
	s.playbackTools(take)
	s.mediaTools(take)
	s.agentTools(take)
	return out
}

// names are the definitions' names.
func names(defs []definition) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.name
	}
	return out
}

// serve builds the per-request server under the calling token's grants: the
// tools its list names (or every tool when it names none).
func (s *Server) serve(grants func(string) bool) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{Name: "cadence", Version: s.d.Version},
		&sdk.ServerOptions{
			Instructions: "Cadence, the household's voice assistant and media server. Devices are named by name " +
				"(or room) in each tool's device argument; list_devices shows them. Media links are token-sealed " +
				"and expire after some hours.",
			Logger:      s.log,
			SchemaCache: s.cache,
		})
	for _, d := range s.definitions(grants) {
		call := d.call
		srv.AddTool(&sdk.Tool{Name: d.name, Description: d.description, InputSchema: d.schema, Annotations: d.annotations},
			func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return call(ctx, req.Params.Arguments)
			})
	}
	return srv
}

// grantsKey carries the fiber layer's token grants into the transport's
// server lookup.
type grantsKey struct{}

// withGrants is ctx with g in it.
func withGrants(ctx context.Context, g grants) context.Context {
	return context.WithValue(ctx, grantsKey{}, g)
}

// grants say which tools the calling token sees.
type grants struct {
	all  bool
	list map[string]bool
}

// sees is whether the tool named is in the grant.
func (g grants) sees(tool string) bool { return g.all || g.list[tool] }

// filter is the definitions' grants argument, nil for every tool.
func (g grants) filter() func(string) bool {
	if g.all {
		return nil
	}
	return g.sees
}

// Tool describes one served tool for the admin list.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Tools are the whole toolset as the admin api lists it, never a caller's
// filtered view.
func (s *Server) Tools() []Tool {
	out := make([]Tool, 0, len(s.toolNames))
	for _, d := range s.definitions(nil) {
		out = append(out, Tool{Name: d.name, Description: d.description})
	}
	return out
}

// Endpoint is the streamable handler behind the api's /mcp route, stateless:
// every request stands alone, one server built for it under its token's
// grants.
func (s *Server) Endpoint() http.Handler {
	return sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		g, _ := r.Context().Value(grantsKey{}).(grants)
		return s.serve(g.filter())
	}, &sdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: s.log,
		SessionTimeout: mcpSessionTimeout,
	})
}

// mcpSessionTimeout closes an idle session; stateless sessions are
// per-request, so this only bounds a stream left open.
const mcpSessionTimeout = 5 * time.Minute

// toolError is a refused call: the client's model reads the code and
// message, never a cause from below.
func toolError(code, message string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{&sdk.TextContent{Text: code + ": " + message}},
	}
}

// textResult is a call's answer as JSON text, the form every client reads.
func textResult(v any) (*sdk.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return toolError("internal", "The answer could not be encoded."), nil
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}, nil
}

// resolveDevice resolves the device a tool's device argument names to its
// id, "" when the argument is empty.
func (s *Server) resolveDevice(ctx context.Context, name string) (string, *sdk.CallToolResult) {
	if name == "" {
		return "", nil
	}
	if s.d.Target != nil {
		t, terr := s.d.Target(ctx, agent.Call{}, name, agent.Need{})
		if terr != nil {
			return "", agentToolError(terr)
		}
		return t.ID, nil
	}
	return name, nil
}
