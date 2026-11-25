package telemetry_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// fakeReader records what the admin asks; slow reads wait for their context,
// and with fail every read fails with it
type fakeReader struct {
	mu      sync.Mutex
	queries []telemetry.LogQuery
	slow    bool
	fail    error
}

func (f *fakeReader) wait(ctx context.Context) error {
	f.mu.Lock()
	slow, fail := f.slow, f.fail
	f.mu.Unlock()
	if !slow {
		return fail
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeReader) Logs(ctx context.Context, q telemetry.LogQuery) ([]telemetry.LogView, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	return []telemetry.LogView{}, f.wait(ctx)
}

func (f *fakeReader) Points(ctx context.Context, _ pgtype.UUID, _ []string, _ time.Time, _ int) ([]telemetry.Point, error) {
	return nil, f.wait(ctx)
}

func (f *fakeReader) Device(_ context.Context, id string) (telemetry.DeviceInfo, error) {
	return telemetry.DeviceInfo{ID: id, Name: "kitchen", Board: "sim"}, nil
}

func (f *fakeReader) last() telemetry.LogQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries[len(f.queries)-1]
}

func newFakeAdmin(t *testing.T, r *fakeReader, timeout time.Duration) (*fiber.App, *syncBuffer) {
	t.Helper()
	out := &syncBuffer{}
	deps := telemetry.AdminDeps{
		Reader: r, Now: func() time.Time { return start }, Timeout: timeout,
		Log:   slog.New(slog.NewJSONHandler(out, nil)),
		State: func(context.Context, string, string) (json.RawMessage, bool) { return nil, false },
	}
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) { telemetry.RegisterAdmin(a, deps) }},
	})
	return app, out
}

func getFake(t *testing.T, app *fiber.App, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer x")
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// a read that runs long gives its connection back: 503 timeout, logged by
// its operation alone
func TestAdminReadsTimeOut(t *testing.T) {
	r := &fakeReader{slow: true}
	app, out := newFakeAdmin(t, r, 50*time.Millisecond)
	dev := "00000000-0000-0000-0000-000000000001"
	for op, path := range map[string]string{
		"logs": "/api/v1/logs?q=rare", "metrics": "/api/v1/devices/" + dev + "/metrics?name=a",
	} {
		began := time.Now()
		st, body := getFake(t, app, path)
		if st != 503 || strings.TrimSpace(body) != `{"error":"timeout"}` || time.Since(began) > 2*time.Second {
			t.Fatalf("%s after %v: %d %s", op, time.Since(began), st, body)
		}
		if logged := out.String(); !strings.Contains(logged, `"msg":"telemetry_admin_failed","op":"`+op+`","code":"timeout"`) ||
			strings.Contains(logged, "rare") {
			t.Fatalf("%s log %s", op, logged)
		}
	}
}

// a store failure is 500 internal, logged by its operation alone: the
// driver's text reaches neither the answer nor the log
func TestAdminReadsFailInternal(t *testing.T) {
	r := &fakeReader{fail: errors.New("pq: relation device_logs secret-driver-text")}
	app, out := newFakeAdmin(t, r, 0)
	dev := "00000000-0000-0000-0000-000000000001"
	for op, path := range map[string]string{
		"logs": "/api/v1/logs", "metrics": "/api/v1/devices/" + dev + "/metrics?name=a",
	} {
		st, body := getFake(t, app, path)
		if st != 500 || strings.TrimSpace(body) != `{"error":"internal"}` {
			t.Fatalf("%s: %d %s", op, st, body)
		}
		if logged := out.String(); !strings.Contains(logged, `"msg":"telemetry_admin_failed","op":"`+op+`","code":"store"`) ||
			strings.Contains(logged, "secret-driver-text") {
			t.Fatalf("%s log %s", op, logged)
		}
	}
}

// a search narrows to the last day unless since says otherwise
func TestLogSearchDefaultsToADay(t *testing.T) {
	r := &fakeReader{}
	app, _ := newFakeAdmin(t, r, 0)
	day, month := start.Add(-24*time.Hour), start.Add(-31*24*time.Hour)
	for query, want := range map[string]time.Time{
		"":                    month,
		"?source=soloist":     month,
		"?q=disk":             day,
		"?level=warn":         day,
		"?q=disk&level=error": day,
		"?q=disk&since=" + url.QueryEscape(start.Add(-72*time.Hour).Format(time.RFC3339)): start.Add(-72 * time.Hour),
	} {
		if st, body := getFake(t, app, "/api/v1/logs"+query); st != 200 {
			t.Fatalf("%s: %d %s", query, st, body)
		}
		if got := r.last().Since; !got.Equal(want) {
			t.Errorf("%s: since %v, want %v", query, got, want)
		}
	}
	// the bounds are bytes, as the store keeps them
	for _, query := range []string{"?q=" + url.QueryEscape(strings.Repeat("é", 200)), "?source=" + url.QueryEscape(strings.Repeat("é", 40))} {
		if st, body := getFake(t, app, "/api/v1/logs"+query); st != 422 {
			t.Errorf("%.40s: %d %s", query, st, body)
		}
	}
	if st, _ := getFake(t, app, "/api/v1/logs?q="+url.QueryEscape(strings.Repeat("é", 128))); st != 200 {
		t.Errorf("256 bytes refused: %d", st)
	}
}
