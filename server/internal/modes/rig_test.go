package modes_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/modes"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

// lines is a log the handlers write while a test reads it
type lines struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type rig struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	modes   *agent.Modes
	prefs   *settings.Service
	app     *fiber.App
	logs    *lines
	dev     string
	changes atomic.Int32
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := storetest.Pool(t)
	r := &rig{t: t, pool: pool, q: db.New(pool), logs: &lines{}}
	log := slog.New(slog.NewTextHandler(r.logs, nil))
	r.modes = agent.NewModes(pool, log)
	r.modes.OnChange(func() { r.changes.Add(1) })
	r.prefs = settings.NewService(pool)
	r.app = api.NewApp(api.Options{
		DB: noPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){
			func(a huma.API) { modes.RegisterAdmin(a, r.modes, log) },
			func(a huma.API) { settings.RegisterAdmin(a, r.prefs) },
		},
	})
	dev, err := r.q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "kitchen", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	r.dev = dev.ID.String()
	return r
}

// call sends an admin request
func (r *rig) call(method, path, body string) (int, string) {
	r.t.Helper()
	return r.as("x", method, path, body)
}

func (r *rig) as(bearer, method, path, body string) (int, string) {
	r.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := r.app.Test(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// must sends an admin request that must answer code
func (r *rig) must(code int, method, path, body string) string {
	r.t.Helper()
	got, b := r.call(method, path, body)
	if got != code {
		r.t.Fatalf("%s %s %s: %d %s, want %d", method, path, body, got, b, code)
	}
	return b
}

func (r *rig) get(name string) agent.Mode {
	r.t.Helper()
	m, err := r.modes.Get(context.Background(), name)
	if err != nil {
		r.t.Fatalf("get %s: %v", name, err)
	}
	return m
}

// a valid body for put-mode
const turbo = `{"model":"some/model","reasoning":"low","maxSteps":3,"maxSeconds":30,"interim":"speak"}`
