package yandex

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

type fakeLogSink struct {
	mu   sync.Mutex
	rows []telemetry.LogRow
	err  error
}

func (f *fakeLogSink) Insert(_ context.Context, rows []telemetry.LogRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, rows...)
	return f.err
}

func TestCaptureLogsStoresScrubbedVendorBatch(t *testing.T) {
	entries := `[{"Timestamp":1763222400,"Path":"/data/quasar/daemons_logs/maind.log","Data":"Authorization: OAuth vendor-token-value capability=cap-secret ?token=query-secret","DeviceId":"vendor-1","Platform":"cucumber"}]`
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(entries))
	_ = writer.Close()
	req := httptest.NewRequest("POST", "https://clck.yandex.net/quasar/write/json/batch?api_key_128=query-secret", &compressed)
	req.Header.Set("Authorization", "OAuth vendor-token-value")
	req.Header.Set("Content-Encoding", "gzip")
	sink := &fakeLogSink{}
	service := &Service{
		logs: sink, devices: map[string]string{"vendor-1": "123e4567-e89b-12d3-a456-426614174000"},
		secrets: func(context.Context, string) (map[string]string, error) {
			return map[string]string{"capability": "cap-secret"}, nil
		},
		now: func() time.Time { return time.Unix(1763222401, 0) },
	}
	stored, err := service.captureLogs(context.Background(), req)
	if err != nil || stored != 1 || len(sink.rows) != 1 {
		t.Fatalf("stored=%d rows=%d err=%v", stored, len(sink.rows), err)
	}
	row := sink.rows[0]
	if row.Source != "yandex-maind.log" || row.DeviceID.String() != "123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("row = %+v", row)
	}
	if !strings.Contains(row.Body, "vendor_device=vendor-1 platform=cucumber") {
		t.Fatalf("body = %q", row.Body)
	}
	for _, secret := range []string{"vendor-token-value", "cap-secret", "query-secret"} {
		if strings.Contains(row.Body, secret) {
			t.Fatalf("%q stored in %q", secret, row.Body)
		}
	}
	if !strings.Contains(row.Body, "redacted") {
		t.Fatalf("no redaction marker in %q", row.Body)
	}
}

func TestCaptureLogsWithoutMappingStaysUnattributed(t *testing.T) {
	req := httptest.NewRequest("POST", "https://clck.yandex.net/quasar/write/json/batch", strings.NewReader(
		`[{"Timestamp":1763222400,"Path":"syncd.log","Data":"ready","DeviceId":"vendor-unknown","Platform":"pickle"}]`))
	sink := &fakeLogSink{}
	service := &Service{logs: sink, now: func() time.Time { return time.Unix(1763222401, 0) }}
	if _, err := service.captureLogs(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(sink.rows) != 1 || sink.rows[0].DeviceID.Valid || sink.rows[0].Body != "vendor_device=vendor-unknown platform=pickle ready" {
		t.Fatalf("rows = %+v", sink.rows)
	}
}

func TestCaptureLogsDoesNotAcknowledgeStoreFailure(t *testing.T) {
	req := httptest.NewRequest("POST", "https://clck.yandex.net/quasar/write/json/batch", strings.NewReader(
		`[{"Timestamp":1763222400,"Path":"maind.log","Data":"ready"}]`))
	service := &Service{logs: &fakeLogSink{err: errors.New("driver secret")}, now: time.Now}
	if _, err := service.captureLogs(context.Background(), req); !errors.Is(err, errTelemetryStore) {
		t.Fatalf("err = %v", err)
	}
}

func TestCaptureLogsRefusesInvalidOrOversizedBatches(t *testing.T) {
	for _, body := range []string{"not-json", `[{"Timestamp":0,"Path":"x","Data":"y"}]`, `[{"Timestamp":1763222400,"Path":"x","Data":"y"}] trailing`} {
		req := httptest.NewRequest("POST", "https://clck.yandex.net/quasar/write/json/batch", strings.NewReader(body))
		service := &Service{logs: &fakeLogSink{}, now: time.Now}
		if _, err := service.captureLogs(context.Background(), req); err == nil {
			t.Fatalf("%q accepted", body)
		}
	}
}

func TestCaptureLogsNormalizesClockAndBody(t *testing.T) {
	req := httptest.NewRequest("POST", "https://clck.yandex.net/quasar/write/json/batch", strings.NewReader(
		`[{"Timestamp":1,"Path":"maind.log","Data":"`+strings.Repeat("x", 5000)+`"}]`))
	sink := &fakeLogSink{}
	now := time.Unix(1763222401, 0)
	service := &Service{logs: sink, now: func() time.Time { return now }}
	if _, err := service.captureLogs(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(sink.rows) != 1 || !sink.rows[0].Time.Equal(now) || !sink.rows[0].Truncated || len(sink.rows[0].Body) > 4096 {
		t.Fatalf("row = %+v", sink.rows)
	}
}

func TestCaptureLogsAcceptsBlankDaemonLine(t *testing.T) {
	req := httptest.NewRequest("POST", "https://clck.yandex.net/quasar/write/json/batch", strings.NewReader(
		`[{"Timestamp":1763222400,"Path":"maind.log","Data":""}]`))
	sink := &fakeLogSink{}
	service := &Service{logs: sink, now: func() time.Time { return time.Unix(1763222401, 0) }}
	if stored, err := service.captureLogs(context.Background(), req); err != nil || stored != 1 {
		t.Fatalf("stored=%d err=%v", stored, err)
	}
}

func TestScrubVendorTextWithoutKnownSecret(t *testing.T) {
	got := scrubVendorText(`token=short Authorization: bearer tiny ?code=abc`)
	for _, secret := range []string{"short", "tiny", "abc"} {
		if strings.Contains(got, secret) {
			t.Fatalf("%q left in %q", secret, got)
		}
	}
}
