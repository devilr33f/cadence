package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/assistant"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/favorites"
	"git.shino.boo/devilreef/cadence/server/internal/history"
	"git.shino.boo/devilreef/cadence/server/internal/mcpserver"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
	"git.shino.boo/devilreef/cadence/server/internal/voice"
)

const devicePropSchema = `{
	"type": "object",
	"properties": {
		"device": {"type": "string", "minLength": 1, "maxLength": 64},
		"value": {"type": "integer", "minimum": 0}
	},
	"required": ["device"],
	"additionalProperties": false
}`

// deviceTool is a fake agent tool that takes a device, as most of the real
// ones do, and records its call.
func deviceTool(name string, seen *[]agent.Call) agent.Tool {
	return agent.Tool{
		Name: name, Description: "A fake tool that echoes its device.",
		Input:   devicePropSchema,
		Effects: agent.Write, Output: agent.Trusted,
		Run: func(_ context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			*seen = append(*seen, c)
			return json.RawMessage(`{"done":true}`), nil
		},
	}
}

// noDeviceTool is a fake agent tool with no device argument.
func noDeviceTool(name string) agent.Tool {
	return agent.Tool{
		Name: name, Description: "A fake tool with no device.",
		Input:   `{"type":"object","properties":{"q":{"type":"string"}},"additionalProperties":false}`,
		Effects: agent.Read, Output: agent.Trusted,
		Run: func(_ context.Context, _ agent.Call) (json.RawMessage, *agent.ToolError) {
			return json.RawMessage(`{"answer":"x"}`), nil
		},
	}
}

// fakePinger stands in for the DB the health route pings.
type fakePinger struct{}

func (fakePinger) Ping(context.Context) error { return nil }

// rig is the endpoint served as the api serves it, with fakes behind every
// seam, driven by the go-sdk's own client.
type rig struct {
	t       *testing.T
	url     string
	srv     *mcpserver.Server
	logs    *lockedBuffer
	mu      sync.Mutex
	calls   []agent.Call
	sent    []sentCommand
	asked   []assistant.Request
	said    []voice.Announcement
	links   int
	targets map[string]string
}

// sentCommand is one command the rig's Send seam saw.
type sentCommand struct {
	DeviceID, Capability, Name string
	Args                       json.RawMessage
}

// lockedBuffer is a log the handlers write while a test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// bearerGrants is the rig's token check: each bearer a fixed grant.
func (r *rig) bearerGrants(_ context.Context, bearer string) (auth.Grants, error) {
	switch bearer {
	case "admin":
		return auth.Grants{Scopes: auth.AdminScopes}, nil
	case "mcp":
		return auth.Grants{Scopes: auth.Scopes{auth.ScopeMCP}}, nil
	case "mcp-two":
		return auth.Grants{Scopes: auth.Scopes{auth.ScopeMCP}, MCPTools: []string{"list_devices", "say"}}, nil
	case "ha":
		return auth.Grants{Scopes: auth.Scopes{auth.ScopeHA}}, nil
	}
	return auth.Grants{}, auth.ErrUnauthorized
}

// newRig assembles the endpoint over fakes and serves it on a local
// listener; edits shape the deps before the server is built.
func newRig(t *testing.T, edit func(*mcpserver.Deps)) *rig {
	t.Helper()
	r := &rig{t: t, logs: &lockedBuffer{}, targets: map[string]string{"kitchen": "dev-1"}}
	registry := agent.NewRegistry(
		deviceTool("fake_write", &r.calls),
		noDeviceTool("fake_read"),
		agent.Tool{Name: "conversation_reset", Description: "A turn's own tool.",
			Input: `{"type":"object","properties":{},"additionalProperties":false}`,
			Run:   func(context.Context, agent.Call) (json.RawMessage, *agent.ToolError) { return nil, nil }},
	)
	d := mcpserver.Deps{
		Version: "t", Log: slog.New(slog.NewTextHandler(r.logs, nil)),
		Tools: registry.Static,
		Target: func(_ context.Context, _ agent.Call, arg string, _ agent.Need) (agent.Target, *agent.ToolError) {
			if id, ok := r.targets[arg]; ok {
				return agent.Target{ID: id, Name: arg}, nil
			}
			return agent.Target{}, &agent.ToolError{Code: "unknown_device", Message: "No device of this household has that name."}
		},
		Settings: func(context.Context, string) (settings.Values, error) { return settings.Values{}, nil },
		List: func(context.Context) ([]enroll.Listed, error) {
			return []enroll.Listed{{ID: "dev-1", Name: "kitchen", Room: "Kitchen", Model: "sim", Status: "enrolled"}}, nil
		},
		Online: func(id string) bool { return id == "dev-1" },
		Detail: func(_ context.Context, id string) (devices.Detail, error) {
			if id != "dev-1" {
				return devices.Detail{}, devices.ErrNotFound
			}
			return devices.Detail{}, nil
		},
		Vitals: func(string) (telemetry.Vitals, bool) { return telemetry.Vitals{}, false },
		Send: func(_ context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error) {
			r.mu.Lock()
			r.sent = append(r.sent, sentCommand{DeviceID: deviceID, Capability: capability, Name: name, Args: append([]byte(nil), args...)})
			r.mu.Unlock()
			return json.RawMessage(`{}`), nil
		},
		History: func(context.Context, string, string, int) (history.HistoryPage, error) {
			return history.HistoryPage{Items: []history.HistoryEntry{{Title: "Song", Artist: "Artist"}}}, nil
		},
		Favorite: func(context.Context, string, int) ([]favorites.Entry, error) { return nil, nil },
		Announce: func(_ context.Context, _ string, a voice.Announcement) (string, error) {
			r.mu.Lock()
			r.said = append(r.said, a)
			r.mu.Unlock()
			return "stream-1", nil
		},
		Ask: func(_ context.Context, req assistant.Request) (agent.Result, string, error) {
			r.mu.Lock()
			r.asked = append(r.asked, req)
			r.mu.Unlock()
			if req.Text == "busy" {
				return agent.Result{}, "", assistant.ErrBusy
			}
			if req.Text == "fail" {
				return agent.Result{Code: "llm_failed"}, "", nil
			}
			return agent.Result{}, "the reply", nil
		},
		Track: func(deviceID, id string) (providers.Track, bool) {
			if deviceID == "dev-1" && id == "r1" {
				return providers.Track{Source: "fake", Instance: "fake-1", ID: "s1", Title: "Song", VideoID: "vid1"}, true
			}
			return providers.Track{}, false
		},
		Music: func(context.Context, string) (providers.Music, error) {
			return fakeStreamer{}, nil
		},
		Player: func(context.Context) (providers.Player, error) { return fakePlayer{}, nil },
		Stream: func(deviceID string, _ providers.Stream) (string, error) {
			r.mu.Lock()
			r.links++
			r.mu.Unlock()
			return "https://pub.example/v2/stream/tok-" + deviceID, nil
		},
		RelayExpiry: func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) },
		ResolveCatalog: func(context.Context, string, string) (catalog.PlayableView, error) {
			return catalog.PlayableView{Kind: "video", Stream: &catalog.StreamView{URL: "https://cat.example/play/1", DurationMS: 90000}}, nil
		},
		Now: func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) },
	}
	if edit != nil {
		edit(&d)
	}
	r.srv = mcpserver.New(d)
	app := api.NewApp(api.Options{
		DB: fakePinger{}, Version: "t", VerifyToken: r.bearerGrants,
		MCP: r.srv.Fiber(),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	r.url = "http://" + ln.Addr().String()
	return r
}

// fakeStreamer streams its own songs.
type fakeStreamer struct{}

func (fakeStreamer) SearchMusic(context.Context, string, int) ([]providers.Track, error) {
	return nil, nil
}

func (fakeStreamer) StreamTrack(context.Context, providers.Track) (providers.Stream, error) {
	return providers.Stream{URL: "https://music.example/s1.mp3", DurationS: 200}, nil
}

// fakePlayer resolves YouTube audio.
type fakePlayer struct{}

func (fakePlayer) Resolve(context.Context, string) (providers.Stream, error) {
	return providers.Stream{URL: "https://audio.example/vid1", DurationS: 100}, nil
}

// rt adds the bearer to every request.
type rt struct {
	bearer string
	next   http.RoundTripper
}

func (a rt) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+a.bearer)
	return a.next.RoundTrip(r)
}

// client is a connected go-sdk client as the bearer.
func (r *rig) client(bearer string) *sdk.ClientSession {
	r.t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "rig", Version: "1"}, &sdk.ClientOptions{})
	cs, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: r.url + "/api/v1/mcp", HTTPClient: &http.Client{Transport: rt{bearer: bearer, next: http.DefaultTransport}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// tools are the tool names a session lists.
func (r *rig) tools(cs *sdk.ClientSession) []string {
	r.t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		r.t.Fatal(err)
	}
	out := make([]string, 0, len(res.Tools))
	for _, t := range res.Tools {
		out = append(out, t.Name)
	}
	return out
}

// call calls one tool and returns its text answer, failing the test on a
// protocol error.
func (r *rig) call(cs *sdk.ClientSession, tool string, args map[string]any) (string, bool) {
	r.t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		r.t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*sdk.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String(), res.IsError
}

// post sends a raw POST as the bearer, for the auth tests.
func (r *rig) post(bearer, body string) (int, string) {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.url+"/api/v1/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
