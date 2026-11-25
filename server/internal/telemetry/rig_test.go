package telemetry_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	collogsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/logs/v1"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	logsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/logs/v1"
	resourcev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/resource/v1"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

const (
	tokenA = "cdd_token-of-device-a-0123456789"
	tokenB = "cdd_token-of-device-b-9876543210"
)

// noon, so a day either side stays in its own UTC day
var start = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// syncBuffer is a log sink the handlers and the test read at once
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type rig struct {
	t     *testing.T
	pool  *pgxpool.Pool
	app   *fiber.App
	in    *telemetry.Ingest
	clock *clock
	logs  *syncBuffer
	devA  string
	devB  string

	mu         sync.Mutex
	secrets    map[string]string // device a's capability secrets
	secretsErr error
	authErr    error
	// how long a token lookup takes, unless its context ends first
	authDelay time.Duration
	tokens    map[string]string // token -> device id
	// runs as the scrub looks its secrets up: past the token check, before the store
	beforeScrub func()
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := storetest.Pool(t)
	q := db.New(pool)
	r := &rig{t: t, pool: pool, clock: &clock{now: start}, logs: &syncBuffer{}}
	for i, p := range []*string{&r.devA, &r.devB} {
		d, err := q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: fmt.Sprint("d", i), Model: "sim"})
		if err != nil {
			t.Fatal(err)
		}
		*p = d.ID.String()
	}
	r.tokens = map[string]string{tokenA: r.devA, tokenB: r.devB}
	r.in = telemetry.NewIngest(telemetry.IngestDeps{
		AuthDevice: func(ctx context.Context, token string) (string, error) {
			r.mu.Lock()
			delay := r.authDelay
			r.mu.Unlock()
			if delay > 0 {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(delay):
				}
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.authErr != nil {
				return "", r.authErr
			}
			if id, ok := r.tokens[token]; ok {
				return id, nil
			}
			return "", auth.ErrUnauthorized
		},
		Secrets: func(_ context.Context, id string) (map[string]string, error) {
			r.mu.Lock()
			hook := r.beforeScrub
			r.mu.Unlock()
			if hook != nil {
				hook()
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if id != r.devA {
				return nil, nil
			}
			return r.secrets, r.secretsErr
		},
		Logs:    telemetry.NewLogStore(q),
		Metrics: telemetry.NewMetricStore(q),
		Now:     r.clock.Now,
		Log:     slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	r.app = fiber.New(fiber.Config{Immutable: true})
	r.in.Mount(r.app.Group("/v2"))
	return r
}

type answer struct {
	status int
	header http.Header
	body   []byte
}

// post sends body as it is: headers name its type, encoding and bearer
func (r *rig) post(path string, body []byte, headers map[string]string) answer {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := r.app.Test(req, fiber.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return answer{status: resp.StatusCode, header: resp.Header, body: b}
}

// send posts a protobuf request the way a device does: gzipped, with its bearer
func (r *rig) send(path, token string, m proto.Message) answer {
	r.t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.post(path, gz(b), map[string]string{
		"Content-Type": "application/x-protobuf", "Content-Encoding": "gzip", "Authorization": "Bearer " + token,
	})
}

func gz(b []byte) []byte {
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	_, _ = w.Write(b)
	_ = w.Close()
	return out.Bytes()
}

func str(s string) *commonv1.AnyValue {
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: s}}
}

func kv(k string, v *commonv1.AnyValue) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: k, Value: v}
}

func flag(k string) *commonv1.KeyValue {
	return kv(k, &commonv1.AnyValue{Value: &commonv1.AnyValue_BoolValue{BoolValue: true}})
}

func unixNano(t time.Time) uint64 { return uint64(t.UnixNano()) } //nolint:gosec // test times are after 1970

// record is a log record as a device sends it: a source attribute and a string body
func record(at time.Time, sev logsv1.SeverityNumber, source, body string, attrs ...*commonv1.KeyValue) *logsv1.LogRecord {
	r := &logsv1.LogRecord{SeverityNumber: sev, SeverityText: "INFO", Body: str(body),
		Attributes: append([]*commonv1.KeyValue{kv("source", str(source))}, attrs...)}
	if !at.IsZero() {
		r.TimeUnixNano = unixNano(at)
	}
	return r
}

// logsRequest wraps records in one resource whose attributes a device forged
func logsRequest(records ...*logsv1.LogRecord) *collogsv1.ExportLogsServiceRequest {
	return &collogsv1.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
			kv("service.name", str("forged")), kv("device.id", str("00000000-0000-0000-0000-000000000009")),
			kv("device.name", str("forged-name")), kv("board", str("forged-board")),
		}},
		ScopeLogs: []*logsv1.ScopeLogs{{Scope: &commonv1.InstrumentationScope{Name: "cadence.device"}, LogRecords: records}},
	}}}
}

const logsPath = "/v2/otlp/v1/logs"

type storedLog struct {
	ts, observed         time.Time
	device               *string
	source, body         string
	severity             int16
	truncated, continued bool
}

func (r *rig) storedLogs() []storedLog {
	r.t.Helper()
	rows, err := r.pool.Query(context.Background(), `SELECT ts, observed_ts, device_id::text, source, severity, body,
		truncated, continued FROM device_logs ORDER BY ts, body`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []storedLog
	for rows.Next() {
		var s storedLog
		if err := rows.Scan(&s.ts, &s.observed, &s.device, &s.source, &s.severity, &s.body, &s.truncated, &s.continued); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (r *rig) set(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn()
}

var errDown = errors.New("store down: password=hunter2 host=10.0.0.9")

// enroll makes one more device and returns its token
func (r *rig) enroll(n int) string {
	r.t.Helper()
	d, err := db.New(r.pool).CreateDevice(context.Background(), db.CreateDeviceParams{Name: fmt.Sprint("enrolled-", n), Model: "sim"})
	if err != nil {
		r.t.Fatal(err)
	}
	token := fmt.Sprintf("cdd_token-of-device-%02d-0123456789", n)
	r.mu.Lock()
	r.tokens[token] = d.ID.String()
	r.mu.Unlock()
	return token
}
