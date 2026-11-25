package telemetry_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

const (
	adminToken   = "cdn_admin-token-of-the-test"
	metricsToken = "cdn_metrics-token-of-the-test"
	haToken      = "cdn_ha-token-of-the-test"
)

// rigTokens are the rig's bearers and their scopes
var rigTokens = map[string]auth.Scopes{
	adminToken: auth.AdminScopes, metricsToken: {auth.ScopeMetrics}, haToken: {auth.ScopeHA},
}

func rigVerify(_ context.Context, bearer string) (auth.Grants, error) {
	if s, ok := rigTokens[bearer]; ok {
		return auth.Grants{Scopes: s}, nil
	}
	return auth.Grants{}, auth.ErrUnauthorized
}

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

// adminRig serves the telemetry admin routes and /metrics over a store with
// two devices: kitchen (yandex-pickle) and office (sim)
type adminRig struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	logs    *telemetry.LogStore
	metrics *telemetry.MetricStore
	reader  *telemetry.Reader
	// bus hears the vitals of each stored round
	bus   *events.Bus
	clock *clock
	// timers runs what the vitals scheduled as the clock reaches it
	timers *timers
	out    *syncBuffer
	app    *fiber.App
	devA   pgtype.UUID
	devB   pgtype.UUID

	mu     sync.Mutex
	states map[string]json.RawMessage
	// series replaces the reader's for /metrics when set
	series func(ctx context.Context, since time.Time, perDevice int) ([]telemetry.Series, error)
}

type adminOption func(*adminRig, *telemetry.PromDeps)

func newAdminRig(t *testing.T, opts ...adminOption) *adminRig {
	t.Helper()
	pool := storetest.Pool(t)
	q := db.New(pool)
	r := &adminRig{t: t, pool: pool, q: q, logs: telemetry.NewLogStore(q), metrics: telemetry.NewMetricStore(q),
		reader: telemetry.NewReader(pool), bus: events.NewBus(16), clock: &clock{now: start}, out: &syncBuffer{},
		states: map[string]json.RawMessage{}}
	r.timers = &timers{clock: r.clock}
	vitals := telemetry.NewVitals(telemetry.VitalsDeps{Bus: r.bus, Now: r.clock.Now, AfterFunc: r.timers.AfterFunc})
	r.metrics.OnInsert(vitals.Observe)
	for _, d := range []struct {
		id          *pgtype.UUID
		name, board string
	}{{&r.devA, "kitchen", "yandex-pickle"}, {&r.devB, "office", "sim"}} {
		row, err := q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: d.name, Model: d.board})
		if err != nil {
			t.Fatal(err)
		}
		*d.id = row.ID
	}
	log := slog.New(slog.NewJSONHandler(r.out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	promDeps := telemetry.PromDeps{
		Series: func(ctx context.Context, since time.Time, perDevice int) ([]telemetry.Series, error) {
			r.mu.Lock()
			fn := r.series
			r.mu.Unlock()
			if fn != nil {
				return fn(ctx, since, perDevice)
			}
			return r.reader.Latest(ctx, since, perDevice)
		},
		Now: r.clock.Now, Log: log,
	}
	for _, o := range opts {
		o(r, &promDeps)
	}
	prom := telemetry.NewProm(promDeps)
	deps := telemetry.AdminDeps{
		Reader: r.reader, Vitals: vitals.Latest, Now: r.clock.Now, Log: log,
		State: func(_ context.Context, id, capability string) (json.RawMessage, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			s, ok := r.states[id+"/"+capability]
			return s, ok
		},
	}
	r.app = api.NewApp(api.Options{
		DB: noPing{}, Version: "t", Log: log,
		VerifyToken: rigVerify,
		Admin:       []func(huma.API){func(a huma.API) { telemetry.RegisterAdmin(a, deps) }},
		Plain:       []api.PlainRoute{prom.Route()},
	})
	return r
}

func (r *adminRig) setState(id pgtype.UUID, capability, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[id.String()+"/"+capability] = json.RawMessage(state)
}

// get asks path with the admin token, or with none for token ""
func (r *adminRig) get(path, token string) (int, http.Header, string) {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func (r *adminRig) insertLogs(rows ...telemetry.LogRow) {
	r.t.Helper()
	if err := r.logs.Insert(context.Background(), rows); err != nil {
		r.t.Fatal(err)
	}
}

func (r *adminRig) insertPoints(rows ...telemetry.MetricRow) {
	r.t.Helper()
	if err := r.metrics.Insert(context.Background(), rows); err != nil {
		r.t.Fatal(err)
	}
}

func line(dev pgtype.UUID, at time.Time, source string, severity int16, body string) telemetry.LogRow {
	return telemetry.LogRow{Time: at, Observed: at.Add(time.Second), DeviceID: dev, Source: source, Severity: severity, Body: body}
}

func point(dev pgtype.UUID, name string, kind int16, attrs string, at time.Time, v float64) telemetry.MetricRow {
	return telemetry.MetricRow{DeviceID: dev, Name: name, Kind: kind, Attrs: []byte(attrs), Time: at, Value: v}
}

// timers holds what the code under test scheduled until advance moves the
// rig's clock to it
type timers struct {
	clock *clock

	mu  sync.Mutex
	due []timer
}

type timer struct {
	at time.Time
	f  func()
}

func (ts *timers) AfterFunc(d time.Duration, f func()) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.due = append(ts.due, timer{at: ts.clock.Now().Add(d), f: f})
}

// advance moves the clock on by d and runs the timers it reached, in order
func (ts *timers) advance(d time.Duration) {
	ts.clock.Add(d)
	now := ts.clock.Now()
	ts.mu.Lock()
	var run, keep []timer
	for _, x := range ts.due {
		if x.at.After(now) {
			keep = append(keep, x)
		} else {
			run = append(run, x)
		}
	}
	ts.due = keep
	ts.mu.Unlock()
	slices.SortStableFunc(run, func(a, b timer) int { return a.at.Compare(b.at) })
	for _, x := range run {
		x.f()
	}
}
