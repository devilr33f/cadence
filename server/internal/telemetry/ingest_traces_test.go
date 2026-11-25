package telemetry_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"google.golang.org/protobuf/proto"

	coltracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/trace/v1"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

const traceDevice = "22222222-2222-2222-2222-222222222222"

// fakeSink records what the ingest hands the trace store.
type fakeSink struct {
	mu       sync.Mutex
	dev      tracing.DeviceRef
	body     []byte
	scrubbed string
	result   tracing.IngestResult
	err      error
}

func (f *fakeSink) Ingest(dev tracing.DeviceRef, body []byte, scrub func(string) string, _ time.Time) (tracing.IngestResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dev, f.body = dev, body
	f.scrubbed = scrub("token " + tokenA + " and sk-cap-secret")
	return f.result, f.err
}

func traceApp(t *testing.T, sink telemetry.TraceSink) *fiber.App {
	t.Helper()
	in := telemetry.NewIngest(telemetry.IngestDeps{
		AuthDevice: func(_ context.Context, token string) (string, error) {
			if token != tokenA {
				return "", auth.ErrUnauthorized
			}
			return traceDevice, nil
		},
		Secrets: func(context.Context, string) (map[string]string, error) {
			return map[string]string{"soloist_key": "sk-cap-secret"}, nil
		},
		Traces: sink,
		Device: func(context.Context, string) (telemetry.DeviceInfo, error) {
			return telemetry.DeviceInfo{ID: traceDevice, Name: "kitchen", Board: "sim"}, nil
		},
		Log: slog.New(slog.DiscardHandler),
	})
	app := fiber.New()
	in.Mount(app.Group("/v2"))
	return app
}

func postTraces(t *testing.T, app *fiber.App, token string, body []byte, gz bool) (int, []byte) {
	t.Helper()
	if gz {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write(body)
		_ = zw.Close()
		body = b.Bytes()
	}
	req := httptest.NewRequest(http.MethodPost, "/v2/otlp/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	if gz {
		req.Header.Set("Content-Encoding", "gzip")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func TestTraceIngest(t *testing.T) {
	sink := &fakeSink{result: tracing.IngestResult{Stored: 3, Rejected: 1}}
	app := traceApp(t, sink)
	body, _ := proto.Marshal(&coltracev1.ExportTraceServiceRequest{})
	for _, token := range []string{"", tokenB} {
		if st, _ := postTraces(t, app, token, body, false); st != http.StatusUnauthorized {
			t.Fatalf("token %q: %d", token, st)
		}
	}
	st, out := postTraces(t, app, tokenA, body, true)
	var resp coltracev1.ExportTraceServiceResponse
	if st != http.StatusOK || proto.Unmarshal(out, &resp) != nil || resp.GetPartialSuccess().GetRejectedSpans() != 1 {
		t.Fatalf("answer %d %+v", st, &resp)
	}
	sink.mu.Lock()
	dev, scrubbed := sink.dev, sink.scrubbed
	sink.mu.Unlock()
	if dev != (tracing.DeviceRef{ID: traceDevice, Name: "kitchen", Board: "sim"}) {
		t.Fatalf("stamp %+v", dev)
	}
	// the request's own token and the device's capability secrets are scrubbed
	if strings.Contains(scrubbed, tokenA) || strings.Contains(scrubbed, "sk-cap-secret") {
		t.Fatalf("scrubbed %q", scrubbed)
	}
	sink.mu.Lock()
	sink.err = tracing.ErrDecode
	sink.mu.Unlock()
	if st, _ := postTraces(t, app, tokenA, []byte{1, 2, 3}, false); st != http.StatusBadRequest {
		t.Fatalf("malformed %d", st)
	}
}

func TestTraceRouteNeedsASink(t *testing.T) {
	in := telemetry.NewIngest(telemetry.IngestDeps{})
	app := fiber.New()
	in.Mount(app.Group("/v2"))
	if st, _ := postTraces(t, app, tokenA, nil, false); st != http.StatusNotFound {
		t.Fatalf("unmounted route %d", st)
	}
}
