package telemetry_test

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	collogsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/logs/v1"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	logsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/logs/v1"
)

func headers(token string) map[string]string {
	return map[string]string{"Content-Type": "application/x-protobuf", "Authorization": "Bearer " + token}
}

func oneLine() []byte {
	b, _ := proto.Marshal(logsRequest(record(start, logsv1.SeverityNumber_SEVERITY_NUMBER_INFO, "cadenced", "hello")))
	return b
}

func TestAuth(t *testing.T) {
	r := newRig(t)
	for name, h := range map[string]map[string]string{
		"missing":    {"Content-Type": "application/x-protobuf"},
		"not bearer": {"Content-Type": "application/x-protobuf", "Authorization": "Basic " + tokenA},
		"empty":      headers(""),
		"bad":        headers("cdd_not-a-token-of-anyone-000000"),
	} {
		if a := r.post(logsPath, oneLine(), h); a.status != 401 || len(a.body) != 0 {
			t.Fatalf("%s: %d %q", name, a.status, a.body)
		}
	}
	if got := r.storedLogs(); len(got) != 0 {
		t.Fatalf("stored %d", len(got))
	}
	// the token store down: the device retries later, and the cause stays out of the log
	r.set(func() { r.authErr = errDown })
	if a := r.post(logsPath, oneLine(), headers(tokenA)); a.status != 503 || len(a.body) != 0 {
		t.Fatalf("store down: %d %q", a.status, a.body)
	}
	r.set(func() { r.authErr = nil })
	if a := r.post(logsPath, oneLine(), headers(tokenB)); a.status != 200 {
		t.Fatalf("good: %d %q", a.status, a.body)
	}
	got := r.storedLogs()
	if len(got) != 1 || got[0].device == nil || *got[0].device != r.devB {
		t.Fatalf("stored %+v", got)
	}
	if l := r.logs.String(); !strings.Contains(l, `"code":"auth"`) || strings.Contains(l, "hunter2") {
		t.Fatalf("log %s", l)
	}
}

func TestContentTypeAndEncoding(t *testing.T) {
	r := newRig(t)
	for _, ct := range []string{"", "application/json", "text/plain", "application/protobuf", "application/x-protobuffer"} {
		h := headers(tokenA)
		h["Content-Type"] = ct
		if a := r.post(logsPath, oneLine(), h); a.status != 415 || len(a.body) != 0 {
			t.Fatalf("%q: %d", ct, a.status)
		}
	}
	for _, enc := range []string{"br", "deflate", "zstd", "gzip, gzip", "x-gzip"} {
		h := headers(tokenA)
		h["Content-Encoding"] = enc
		if a := r.post(logsPath, gz(oneLine()), h); a.status != 415 {
			t.Fatalf("%q: %d", enc, a.status)
		}
	}
	// identity and a parameter on the type are fine, and so are gzip and its case
	h := headers(tokenA)
	h["Content-Type"] = "application/x-protobuf; charset=binary"
	h["Content-Encoding"] = "identity"
	if a := r.post(logsPath, oneLine(), h); a.status != 200 {
		t.Fatalf("identity: %d", a.status)
	}
	h["Content-Encoding"] = "GZIP"
	if a := r.post(logsPath, gz(oneLine()), h); a.status != 200 {
		t.Fatalf("gzip: %d", a.status)
	}
	if got := r.storedLogs(); len(got) != 2 {
		t.Fatalf("stored %d", len(got))
	}
}

func TestMalformedIs400(t *testing.T) {
	r := newRig(t)
	for name, body := range map[string][]byte{
		"truncated varint": {0x0a, 0xff},
		"length past end":  {0x0a, 0x10, 0x01},
		"group":            {0x0b, 0x0c},
		"inner garbage":    append([]byte{0x0a, 0x04, 0x12, 0x02, 0x12, 0x05}, 0),
	} {
		if a := r.post(logsPath, body, headers(tokenA)); a.status != 400 || len(a.body) != 0 {
			t.Fatalf("%s: %d", name, a.status)
		}
	}
	h := headers(tokenA)
	h["Content-Encoding"] = "gzip"
	if a := r.post(logsPath, []byte("not gzip at all"), h); a.status != 400 {
		t.Fatalf("bad gzip: %d", a.status)
	}
	if got := r.storedLogs(); len(got) != 0 {
		t.Fatalf("stored %d", len(got))
	}
}

func TestSizeCaps(t *testing.T) {
	r := newRig(t)
	// 2 MiB of noise, gzipped (it stays over 2 MiB) and plain
	noise := make([]byte, 2<<20+1)
	_, _ = rand.Read(noise)
	h := headers(tokenA)
	if a := r.post(logsPath, noise, h); a.status != 413 {
		t.Fatalf("plain over 2 MiB: %d", a.status)
	}
	h["Content-Encoding"] = "gzip"
	if a := r.post(logsPath, gz(noise), h); a.status != 413 {
		t.Fatalf("gzip over 2 MiB: %d", a.status)
	}
	// a body that decompresses past 16 MiB
	big := bytes.Repeat([]byte{0}, 16<<20+1)
	if a := r.post(logsPath, gz(big), h); a.status != 413 {
		t.Fatalf("decompressed over 16 MiB: %d", a.status)
	}
	// exactly 16 MiB passes the cap (it is no OTLP, so 400)
	r.clock.Add(time.Hour)
	if a := r.post(logsPath, gz(big[:16<<20]), h); a.status != 400 {
		t.Fatalf("16 MiB: %d", a.status)
	}
}

// a gzip bomb: 256 MiB of zeros in a quarter of a MiB; the server reads 16 MiB of it at most
func TestGzipBombNeverAllocatesPastTheBound(t *testing.T) {
	r := newRig(t)
	var bomb bytes.Buffer
	w, _ := gzip.NewWriterLevel(&bomb, gzip.BestCompression)
	zeros := make([]byte, 1<<20)
	for range 256 {
		_, _ = w.Write(zeros)
	}
	_ = w.Close()
	if bomb.Len() > 2<<20 {
		t.Fatalf("bomb of %d bytes", bomb.Len())
	}
	h := headers(tokenA)
	h["Content-Encoding"] = "gzip"
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	a := r.post(logsPath, bomb.Bytes(), h)
	runtime.ReadMemStats(&after)
	if a.status != 413 {
		t.Fatalf("bomb: %d", a.status)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
		t.Fatalf("allocated %d MiB for a bomb", grew>>20)
	}
}

// a gzipped request of a little over n decompressed bytes, whose one body is filler
func sized(n int) []byte {
	b, _ := proto.Marshal(logsRequest(record(start, logsv1.SeverityNumber_SEVERITY_NUMBER_INFO, "cadenced", strings.Repeat("x", n))))
	return gz(b)
}

func gzipped(token string) map[string]string {
	h := headers(token)
	h["Content-Encoding"] = "gzip"
	return h
}

func TestRateLimit(t *testing.T) {
	r := newRig(t)
	// 8 MiB of burst, each request let in while its compressed size is left and
	// charged its decompressed one: eight of a little over 1 MiB pass, which
	// leaves the bucket just in debt, and the ninth waits
	body := sized(1 << 20)
	for i := range 8 {
		if a := r.post(logsPath, body, gzipped(tokenA)); a.status != 200 {
			t.Fatalf("request %d: %d", i, a.status)
		}
	}
	a := r.post(logsPath, body, gzipped(tokenA))
	if a.status != 429 || a.header.Get("Retry-After") != "1" || len(a.body) != 0 {
		t.Fatalf("over the burst: %d retry-after %q", a.status, a.header.Get("Retry-After"))
	}
	// identity bodies are charged whole up front: 1 MiB plain waits too
	if a := r.post(logsPath, oneLine(), headers(tokenA)); a.status != 429 {
		t.Fatalf("plain in debt: %d", a.status)
	}
	// another device has its own bucket
	if a := r.post(logsPath, body, gzipped(tokenB)); a.status != 200 {
		t.Fatalf("other device: %d", a.status)
	}
	// it refills at 1 MiB/s
	r.clock.Add(time.Second)
	if a := r.post(logsPath, body, gzipped(tokenA)); a.status != 200 {
		t.Fatalf("after the wait: %d", a.status)
	}
	// 6 MiB, three times: the second leaves 4 MiB of debt, the third waits for it
	big := sized(6 << 20)
	r.clock.Add(time.Hour)
	for i := range 2 {
		if a := r.post(logsPath, big, gzipped(tokenA)); a.status != 200 {
			t.Fatalf("big %d: %d", i, a.status)
		}
	}
	a = r.post(logsPath, big, gzipped(tokenA))
	if a.status != 429 {
		t.Fatalf("big again: %d", a.status)
	}
	if s, _ := strconv.Atoi(a.header.Get("Retry-After")); s != 5 {
		t.Fatalf("retry-after %q", a.header.Get("Retry-After"))
	}
	// a request larger than the burst passes too, and leaves the bucket in debt
	huge := sized(12 << 20)
	r.clock.Add(time.Hour)
	if a := r.post(logsPath, huge, gzipped(tokenA)); a.status != 200 {
		t.Fatalf("huge: %d", a.status)
	}
	r.clock.Add(3 * time.Second)
	if a := r.post(logsPath, body, gzipped(tokenA)); a.status != 429 {
		t.Fatalf("in debt: %d", a.status)
	}
	if l := r.logs.String(); strings.Contains(l, "xxxx") {
		t.Fatal("a body reached the log")
	}
}

func TestLogsStoredAndStamped(t *testing.T) {
	r := newRig(t)
	const info, warn = logsv1.SeverityNumber_SEVERITY_NUMBER_INFO, logsv1.SeverityNumber_SEVERITY_NUMBER_WARN
	early := start.Add(-23 * time.Hour) // yesterday: its partition is made on demand
	req := logsRequest(
		record(start.Add(-time.Second), info, "cadenced", `{"msg":"link_open"}`),
		record(early, warn, "soloist", "half a li…", flag("truncated")),
		record(start.Add(-500*time.Millisecond), warn, "soloist", "…ne", flag("continued")),
		record(time.Unix(5, 0), info, "kmsg", "a 1970 clock"),
		record(start.Add(25*time.Hour), info, "kmsg", "a clock a day ahead"),
		record(time.Time{}, logsv1.SeverityNumber_SEVERITY_NUMBER_ERROR, "audiod", "no time at all"),
	)
	observed := record(time.Time{}, info, "audiod", "observed only")
	observed.ObservedTimeUnixNano = unixNano(start.Add(-2 * time.Second))
	req.ResourceLogs = append(req.ResourceLogs, &logsv1.ResourceLogs{ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: []*logsv1.LogRecord{observed}}}})
	a := r.send(logsPath, tokenA, req)
	if a.status != 200 || len(a.body) != 0 || a.header.Get("Content-Type") != "application/x-protobuf" {
		t.Fatalf("%d %q %q", a.status, a.body, a.header.Get("Content-Type"))
	}
	got := r.storedLogs()
	want := []storedLog{
		{ts: early, source: "soloist", severity: 13, body: "half a li…", truncated: true},
		{ts: start.Add(-2 * time.Second), source: "audiod", severity: 9, body: "observed only"},
		{ts: start.Add(-time.Second), source: "cadenced", severity: 9, body: `{"msg":"link_open"}`},
		{ts: start.Add(-500 * time.Millisecond), source: "soloist", severity: 13, body: "…ne", continued: true},
		{ts: start, source: "kmsg", severity: 9, body: "a 1970 clock"},
		{ts: start, source: "kmsg", severity: 9, body: "a clock a day ahead"},
		{ts: start, source: "audiod", severity: 17, body: "no time at all"},
	}
	if len(got) != len(want) {
		t.Fatalf("stored %d: %+v", len(got), got)
	}
	for i, w := range want {
		g := got[i]
		if !g.ts.Equal(w.ts) || !g.observed.Equal(start) || g.device == nil || *g.device != r.devA || g.source != w.source ||
			g.severity != w.severity || g.body != w.body || g.truncated != w.truncated || g.continued != w.continued {
			t.Fatalf("row %d: %+v, want %+v", i, g, w)
		}
	}
	// the 1970 record made no partition of its own
	var parts []string
	rows, _ := r.pool.Query(t.Context(), `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = 'device_logs'::regclass ORDER BY 1`)
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		parts = append(parts, p)
	}
	rows.Close()
	if strings.Join(parts, ",") != "device_logs_20261110,device_logs_20261111" {
		t.Fatalf("partitions %v", parts)
	}
}

func TestLogsPartialSuccess(t *testing.T) {
	r := newRig(t)
	const info = logsv1.SeverityNumber_SEVERITY_NUMBER_INFO
	noSource := record(start, info, "x", "no source")
	noSource.Attributes = nil
	intBody := record(start, info, "cadenced", "")
	intBody.Body = &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: 7}}
	noBody := record(start, info, "cadenced", "")
	noBody.Body = nil
	badSeverity := record(start, info, "cadenced", "severity 99")
	badSeverity.SeverityNumber = 99
	a := r.send(logsPath, tokenA, logsRequest(
		record(start, info, "cadenced", "kept"),
		noSource, intBody, noBody, badSeverity,
		record(start, info, "server", "claims the server's source"),
		record(start, info, strings.Repeat("s", 65), "source too long"),
		record(start, info, "nul\x00source", "source with nul"),
		record(start, info, strings.Repeat("s", 64), "longest source"),
	))
	var resp collogsv1.ExportLogsServiceResponse
	if a.status != 200 || proto.Unmarshal(a.body, &resp) != nil {
		t.Fatalf("%d", a.status)
	}
	if ps := resp.GetPartialSuccess(); ps.GetRejectedLogRecords() != 7 || ps.GetErrorMessage() != "some log records were rejected" {
		t.Fatalf("partial success %+v", ps)
	}
	got := r.storedLogs()
	if len(got) != 2 || got[0].body != "kept" && got[1].body != "kept" {
		t.Fatalf("stored %+v", got)
	}
	// nothing to store at all is still a success
	if a := r.send(logsPath, tokenA, &collogsv1.ExportLogsServiceRequest{}); a.status != 200 || len(a.body) != 0 {
		t.Fatalf("empty: %d %q", a.status, a.body)
	}
}

func TestLogsBodyKeptBounded(t *testing.T) {
	r := newRig(t)
	const info = logsv1.SeverityNumber_SEVERITY_NUMBER_INFO
	r.send(logsPath, tokenA, logsRequest(record(start, info, "kmsg", strings.Repeat("é", 5000))))
	got := r.storedLogs()
	if len(got) != 1 || len(got[0].body) > 4096 || !strings.HasSuffix(got[0].body, "é…") || !got[0].truncated {
		t.Fatalf("stored %d bytes, truncated %v", len(got[0].body), got[0].truncated)
	}
}

func TestServerScrub(t *testing.T) {
	r := newRig(t)
	const key = "soloist-api-key/with+chars=="
	r.set(func() { r.secrets = map[string]string{"media.soloist.apiKey": key, "voice.short": "short"} })
	const info = logsv1.SeverityNumber_SEVERITY_NUMBER_INFO
	a := r.send(logsPath, tokenA, logsRequest(
		record(start, info, "soloist", "1 Authorization: Bearer "+tokenA),
		record(start, info, "soloist", "2 --api-key "+key+" --ws"),
		record(start, info, "soloist", "3 url ?k="+url.QueryEscape(key)),
		record(start, info, "soloist", "4 b64 "+base64.StdEncoding.EncodeToString([]byte(tokenA))),
		record(start, info, "soloist", "5 raw "+base64.RawURLEncoding.EncodeToString([]byte(key))),
		record(start, info, "soloist", "6 cut --api-key "+key[:10], flag("truncated")),
		record(start, info, "soloist", key[len(key)-6:]+" 7 continued", flag("continued")),
		record(start, info, "soloist", "8 short stays short"),
		record(start, info, "soloist", "9 the other device's "+tokenB),
	))
	if a.status != 200 {
		t.Fatalf("%d", a.status)
	}
	want := map[string]string{
		"1": "1 Authorization: Bearer «redacted:device_token»",
		"2": "2 --api-key «redacted:media.soloist.apiKey» --ws",
		"3": "3 url ?k=«redacted:media.soloist.apiKey»",
		"4": "4 b64 «redacted:device_token»",
		"5": "5 raw «redacted:media.soloist.apiKey»",
		"6": "6 cut --api-key «redacted:media.soloist.apiKey»…",
		"7": "…«redacted:media.soloist.apiKey» 7 continued",
		"8": "8 short stays short",
		// a token is scrubbed in its own device's lines: b's is no secret a holds
		"9": "9 the other device's " + tokenB,
	}
	got := r.storedLogs()
	if len(got) != len(want) {
		t.Fatalf("stored %d", len(got))
	}
	for _, g := range got {
		n := strings.TrimPrefix(strings.Fields(g.body)[0], "…")
		if n == "«redacted:media.soloist.apiKey»" {
			n = "7"
		}
		if g.body != want[n] {
			t.Fatalf("line %s stored as %q", n, g.body)
		}
		if strings.Contains(g.body, key) || strings.Contains(g.body, tokenA) {
			t.Fatalf("secret stored: %q", g.body)
		}
	}
	// the secrets cannot be read: nothing is stored unscrubbed, the device retries
	r.set(func() { r.secretsErr = errDown })
	if a := r.send(logsPath, tokenA, logsRequest(record(start, info, "soloist", "--api-key "+key))); a.status != 503 {
		t.Fatalf("secrets down: %d", a.status)
	}
	if n := len(r.storedLogs()); n != len(want) {
		t.Fatalf("stored %d", n)
	}
	if l := r.logs.String(); strings.Contains(l, key) || strings.Contains(l, tokenA) || strings.Contains(l, "hunter2") ||
		!strings.Contains(l, `"code":"secrets"`) {
		t.Fatalf("log %s", l)
	}
}

// the server's own lines about ingest carry codes and counts, never a body, a token or an attribute
func TestIngestLogsCarryCountsOnly(t *testing.T) {
	r := newRig(t)
	const info = logsv1.SeverityNumber_SEVERITY_NUMBER_INFO
	r.send(logsPath, tokenA, logsRequest(record(start, info, "soloist", "private body text"), record(start, info, "", "x")))
	r.post(logsPath, []byte{0x0a, 0xff}, headers(tokenA))
	l := r.logs.String()
	for _, want := range []string{
		`"msg":"otlp_ingest"`, `"kind":"logs"`, `"device_id":"` + r.devA + `"`, `"records":1`, `"rejected":1`, `"bytes":`,
		`"msg":"otlp_ingest_failed"`, `"code":"decode"`,
	} {
		if !strings.Contains(l, want) {
			t.Fatalf("log lacks %s: %s", want, l)
		}
	}
	for _, bad := range []string{"private", tokenA, "soloist", "forged"} {
		if strings.Contains(l, bad) {
			t.Fatalf("log carries %q: %s", bad, l)
		}
	}
}

// a device that keeps failing logs one warning a minute, counting the ones held back
func TestWarningsThrottledPerDevice(t *testing.T) {
	r := newRig(t)
	bad := []byte{0x0a, 0xff}
	for range 3 {
		r.post(logsPath, bad, headers(tokenA))
	}
	r.post(logsPath, bad, headers(tokenB))
	r.clock.Add(time.Minute)
	r.post(logsPath, bad, headers(tokenA))
	l := r.logs.String()
	if n := strings.Count(l, `"msg":"otlp_ingest_failed"`); n != 3 {
		t.Fatalf("%d warnings: %s", n, l)
	}
	if !strings.Contains(l, `"device_id":"`+r.devA+`","code":"decode","suppressed":2`) ||
		!strings.Contains(l, `"device_id":"`+r.devB+`","code":"decode","suppressed":0`) {
		t.Fatalf("log %s", l)
	}
}

// a request's records past 128 Ki are rejected, not held
func TestLogsRecordCap(t *testing.T) {
	r := newRig(t)
	recs := make([]*logsv1.LogRecord, 128<<10+1)
	for i := range recs {
		recs[i] = &logsv1.LogRecord{TimeUnixNano: unixNano(start), SeverityNumber: logsv1.SeverityNumber_SEVERITY_NUMBER_INFO,
			Body: str("b"), Attributes: []*commonv1.KeyValue{kv("source", str("s"))}}
	}
	a := r.send(logsPath, tokenA, logsRequest(recs...))
	var resp collogsv1.ExportLogsServiceResponse
	if a.status != 200 || proto.Unmarshal(a.body, &resp) != nil || resp.GetPartialSuccess().GetRejectedLogRecords() != 1 {
		t.Fatalf("%d %v", a.status, resp.GetPartialSuccess())
	}
	if n := countLogs(t, r.pool); n != 128<<10 {
		t.Fatalf("stored %d", n)
	}
}
