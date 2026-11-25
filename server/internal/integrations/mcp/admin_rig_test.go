package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
)

const (
	adminToken = "admin-token-4e1a-never-logged"
	// noteHeader rides on every admin request
	noteHeader = "Bearer secret-header-8c1d"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

// adminRig serves the admin routes over a catalog, an agent registry that
// reads it, and a stand-in for the settings and the instances.
type adminRig struct {
	*catRig
	app *fiber.App
	reg *agent.Registry

	mu sync.Mutex
	// setting is the global mcp setting
	setting []string
	// others are the listed names that are no instance of kind mcp
	others   map[string]bool
	disabled map[string]string
	down     error
}

func newAdmin(t *testing.T, builtins ...agent.Tool) *adminRig {
	t.Helper()
	r := &adminRig{catRig: newCatalog(t), others: map[string]bool{}, disabled: map[string]string{}}
	log := slog.New(slog.NewTextHandler(r.logs, nil))
	r.reg = agent.NewRegistry(builtins...)
	r.reg.AddSource(mcp.SourceName, mcp.NewSource(r.cat, log))
	adm := mcp.NewAdmin(mcp.AdminDeps{
		Catalog: r.cat,
		Listed: func(context.Context) ([]string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return slices.Clone(r.setting), r.down
		},
		IsMCP: func(_ context.Context, name string) (bool, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return !r.others[name], r.down
		},
		Disabled: func(name string) string {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.disabled[name]
		},
		Skips: r.reg.Skips,
		Log:   log,
	})
	r.app = api.NewApp(api.Options{
		DB: noPing{}, Version: "t", Log: log,
		VerifyToken: apitest.Only(adminToken),
		Admin:       []func(huma.API){func(h huma.API) { mcp.RegisterAdmin(h, adm) }},
	})
	return r
}

// list sets the mcp setting and runs the servers given.
func (r *adminRig) list(names []string, run ...mcp.Server) {
	r.mu.Lock()
	r.setting = names
	r.mu.Unlock()
	r.cat.Apply(run)
}

func (r *adminRig) do(method, path, body string) (int, string) {
	r.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, rd)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("X-Note", noteHeader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// want checks a request's status and body.
func (r *adminRig) want(method, path, body string, status int, want string) {
	r.t.Helper()
	if got, b := r.do(method, path, body); got != status || b != want {
		r.t.Fatalf("%s %s %s: %d %s, want %d %s", method, path, body, got, b, status, want)
	}
}

type countsBody struct{ Total, Offered, Changed, Skipped int }

type serverBody struct {
	Instance, Status, Host, Era, LastCode string
	FailingSince, LastRefresh             *time.Time
	Tools                                 countsBody
}

type hintsBody struct{ ReadOnly, Destructive, Idempotent, OpenWorld *bool }

type toolBody struct {
	Remote, Local, Description string
	Hints                      hintsBody
	SchemaBytes                int
	Effects, Trust             string
	EnabledDefault, AnyTurn    bool
	Changed                    bool
	Hash, AckedHash            string
	FirstSeen, LastSeen        *time.Time
	Skip                       string
}

func decodeAs[T any](t *testing.T, what string, status int, body string) T {
	t.Helper()
	var out T
	if status != 200 || json.Unmarshal([]byte(body), &out) != nil {
		t.Fatalf("%s: %d %s", what, status, body)
	}
	return out
}

func (r *adminRig) servers() map[string]serverBody {
	r.t.Helper()
	code, b := r.do("GET", "/mcp/servers", "")
	out := map[string]serverBody{}
	for _, s := range decodeAs[[]serverBody](r.t, "servers", code, b) {
		out[s.Instance] = s
	}
	return out
}

func (r *adminRig) serverOf(name string) serverBody {
	r.t.Helper()
	s, ok := r.servers()[name]
	if !ok {
		r.t.Fatalf("no server %s", name)
	}
	return s
}

func (r *adminRig) tools(name string) []toolBody {
	r.t.Helper()
	code, b := r.do("GET", "/mcp/servers/"+name+"/tools", "")
	return decodeAs[[]toolBody](r.t, name+" tools", code, b)
}

func toolOf(t *testing.T, tools []toolBody, remote string) toolBody {
	t.Helper()
	i := slices.IndexFunc(tools, func(tb toolBody) bool { return tb.Remote == remote })
	if i < 0 {
		t.Fatalf("no tool %q in %+v", remote, tools)
	}
	return tools[i]
}

func (r *adminRig) patch(name, remote, body string) (int, string) {
	r.t.Helper()
	return r.do("PATCH", "/mcp/servers/"+name+"/tools/"+url.PathEscape(remote), body)
}

// offered is the tool names a woken turn of a device whose tools setting is
// device (nil: unset) gets now.
func (r *adminRig) offered(device []string) []string {
	var out []string
	for _, tl := range r.reg.Offer(device, false, cadencev2.Trigger_TRIGGER_WAKE) {
		out = append(out, tl.Name)
	}
	return out
}

var errDown = errors.New("db down secret-db-text")
