package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp/otlptest"
	colmetricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/metrics/v1"
)

const testToken = "cdd_token-for-the-test"

func enrolled(t *testing.T) config.Config {
	t.Helper()
	return config.Config{Board: "sim", ServerURL: "https://127.0.0.1:1", DeviceID: "dev-1", DeviceToken: testToken, DataDir: t.TempDir()}
}

func TestStartLogsKnowsTheToken(t *testing.T) {
	p, secrets, _, stop := startLogs(context.Background(), enrolled(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer stop()
	if p == nil || secrets == nil {
		t.Fatal("no pipeline for an enrolled device")
	}
	if got := secrets.Scrub("auth " + testToken); got != "auth «redacted:device_token»" {
		t.Fatalf("scrubbed = %q", got)
	}
}

func TestStartLogsOnlyWhenEnrolled(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	offline := config.Config{Board: "sim", DataDir: t.TempDir()}
	p, secrets, _, stop := startLogs(context.Background(), offline, log)
	stop()
	if p != nil || secrets != nil {
		t.Fatal("a pipeline for an offline device")
	}
	// core refuses the link over the same file and says why
	bad := enrolled(t)
	bad.CAFile = filepath.Join(t.TempDir(), "missing.pem")
	p, secrets, _, stop = startLogs(context.Background(), bad, log)
	stop()
	if p != nil || secrets != nil {
		t.Fatal("a pipeline without its ca file")
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// cadenced's lines reach the server once; the pipeline's own trouble (a refused batch) goes to the local log alone,
// never back into the pipeline
func TestPipelineTroubleStaysLocal(t *testing.T) {
	srv := otlptest.New(t)
	srv.Respond(func(otlptest.Request) otlptest.Response { return otlptest.Response{Status: http.StatusBadRequest} })
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := enrolled(t)
	cfg.ServerURL, cfg.CAFile = srv.URL, ca
	out := &lockedBuf{}
	lg := newLogging(out)
	p, _, _, stop := startLogs(context.Background(), cfg, lg.local)
	defer stop()
	lg.tee.Attach(p)
	lg.log.Info("board_ready", "board", "sim")

	logs := otlptest.Logs(t, srv.Next(t))
	recs := logs.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()
	if len(recs) != 1 || !strings.Contains(recs[0].GetBody().GetStringValue(), `"msg":"board_ready"`) {
		t.Fatalf("records %v", recs)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "logs_rejected") {
		if time.Now().After(deadline) {
			t.Fatalf("no logs_rejected in %q", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := p.Stats(); st.Rejected != 1 || st.RingBytes != 0 {
		t.Fatalf("stats %+v: the pipeline's own line went into it", st)
	}
	srv.None(t, 3*time.Second)
	if n := strings.Count(out.String(), "board_ready"); n != 1 {
		t.Fatalf("board_ready written %d times:\n%s", n, out.String())
	}
}

// the metrics ship through the client the logs ship through, with the device token, and count the pipeline's drops,
// refusals and cut lines beside cadenced's own
func TestMetricsShipThroughTheLogsClient(t *testing.T) {
	srv := otlptest.New(t)
	srv.Respond(func(r otlptest.Request) otlptest.Response {
		if r.Path == otlp.LogsPath {
			return otlptest.Response{Status: http.StatusBadRequest}
		}
		return otlptest.Response{Status: http.StatusOK}
	})
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := enrolled(t)
	cfg.ServerURL, cfg.CAFile = srv.URL, ca
	p, _, client, stop := startLogs(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer stop()
	if client == nil {
		t.Fatal("no client")
	}
	p.Emit(logs.Record{Severity: logs.Info, Source: "cadenced", Body: "board_ready"})
	if r := srv.Next(t); r.Path != otlp.LogsPath || r.Header.Get("Authorization") != "Bearer "+testToken {
		t.Fatalf("request %s %v", r.Path, r.Header)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.Stats().Rejected != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the batch was not refused")
		}
		time.Sleep(5 * time.Millisecond)
	}

	env := metricsEnv(client, p)
	m := telemetry.NewMetrics(telemetry.MetricsOptions{Env: env, Proc: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { m.Run(ctx) })
	defer func() { cancel(); wg.Wait() }()
	r := srv.Next(t)
	if r.Path != otlp.MetricsPath || r.Header.Get("Authorization") != "Bearer "+testToken {
		t.Fatalf("request %s %v", r.Path, r.Header)
	}
	var req colmetricsv1.ExportMetricsServiceRequest
	if err := proto.Unmarshal(r.Body, &req); err != nil {
		t.Fatal(err)
	}
	var rejected int64 = -1
	for _, mt := range req.GetResourceMetrics()[0].GetScopeMetrics()[0].GetMetrics() {
		if mt.GetName() == "cadence.logs_rejected" {
			rejected = mt.GetSum().GetDataPoints()[0].GetAsInt()
		}
	}
	if rejected != 1 {
		t.Fatalf("logs rejected %d", rejected)
	}
}
