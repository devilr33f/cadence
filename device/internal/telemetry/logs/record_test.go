package logs_test

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	logsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/logs/v1"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// offline is a pipeline that is never run: what it emits stays in its ring
func offline(t *testing.T) (*logs.Pipeline, *fakeClock) {
	t.Helper()
	clk := newFakeClock(t0)
	p := logs.New(nil, &logs.Scrubber{}, discard())
	logs.SetClock(p, clk)
	return p, clk
}

func held(t *testing.T, p *logs.Pipeline) []*logsv1.LogRecord {
	t.Helper()
	recs, err := logs.Held(p)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func one(t *testing.T, p *logs.Pipeline, r logs.Record) *logsv1.LogRecord {
	t.Helper()
	before := len(held(t, p))
	p.Emit(r)
	recs := held(t, p)
	if len(recs) != before+1 {
		t.Fatalf("records = %d, want %d", len(recs), before+1)
	}
	return recs[len(recs)-1]
}

func source(r *logsv1.LogRecord) string {
	for _, kv := range r.GetAttributes() {
		if kv.GetKey() == "source" {
			return kv.GetValue().GetStringValue()
		}
	}
	return ""
}

func TestRecordFields(t *testing.T) {
	p, _ := offline(t)
	at := t0.Add(-time.Minute)
	got := one(t, p, logs.Record{Time: at, Severity: logs.Warn, Source: "kmsg", Body: "usb 1-1: new device"})
	if got.GetTimeUnixNano() != uint64(at.UnixNano()) || got.GetBody().GetStringValue() != "usb 1-1: new device" {
		t.Fatalf("record = %v", got)
	}
	if len(got.GetAttributes()) != 1 || source(got) != "kmsg" {
		t.Fatalf("attributes = %v", got.GetAttributes())
	}
}

// a record without a time gets the time it was emitted
func TestRecordWithoutTime(t *testing.T) {
	p, _ := offline(t)
	if got := one(t, p, logs.Record{Severity: logs.Info, Source: "cadenced", Body: "x"}); got.GetTimeUnixNano() != uint64(t0.UnixNano()) {
		t.Fatalf("time = %d", got.GetTimeUnixNano())
	}
}

func TestSeverityMapping(t *testing.T) {
	p, _ := offline(t)
	p.SetLevels(nil, logs.Debug)
	for _, c := range []struct {
		sev  logs.Severity
		num  logsv1.SeverityNumber
		text string
	}{
		{logs.Debug, 5, "DEBUG"},
		{logs.Info, 9, "INFO"},
		{logs.Warn, 13, "WARN"},
		{logs.Error, 17, "ERROR"},
		// numbers between the four fall into the level below them
		{logs.Severity(11), 9, "INFO"},
		{logs.Severity(24), 17, "ERROR"},
		{logs.Severity(0), 5, "DEBUG"},
	} {
		got := one(t, p, logs.Record{Severity: c.sev, Source: "cadenced", Body: "x"})
		if got.GetSeverityNumber() != c.num || got.GetSeverityText() != c.text {
			t.Fatalf("severity %d = %v %q", c.sev, got.GetSeverityNumber(), got.GetSeverityText())
		}
	}
}

func TestBodyCut(t *testing.T) {
	p, _ := offline(t)
	exact := strings.Repeat("a", 4096)
	if got := one(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: exact}).GetBody().GetStringValue(); got != exact {
		t.Fatalf("4096 bytes cut to %d", len(got))
	}
	long := one(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: strings.Repeat("a", 5000)}).GetBody().GetStringValue()
	if len(long) != 4096 || !strings.HasSuffix(long, "…") || strings.TrimSuffix(long, "…") != strings.Repeat("a", 4093) {
		t.Fatalf("cut = %d bytes, suffix %q", len(long), long[len(long)-8:])
	}
	// two-byte runes: the cut never splits one
	wide := one(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: strings.Repeat("é", 3000)}).GetBody().GetStringValue()
	if len(wide) > 4096 || !utf8.ValidString(wide) || !strings.HasSuffix(wide, "é…") {
		t.Fatalf("cut = %d bytes, valid %v", len(wide), utf8.ValidString(wide))
	}
}

func TestInvalidUTF8Replaced(t *testing.T) {
	p, _ := offline(t)
	got := one(t, p, logs.Record{Severity: logs.Info, Source: "s\xff", Body: "ok\xff\xfeok"})
	if b := got.GetBody().GetStringValue(); b != "ok�ok" {
		t.Fatalf("body = %q", b)
	}
	if s := source(got); s != "s�" {
		t.Fatalf("source = %q", s)
	}
}

// a secret is redacted before the record is cut, so one across the cut is never half kept
func TestScrubbedBeforeCut(t *testing.T) {
	s := &logs.Scrubber{}
	s.Set("device_token", "cdd_0123456789abcdef")
	p := logs.New(nil, s, discard())
	body := strings.Repeat("a", 4085) + "cdd_0123456789abcdef"
	p.Emit(logs.Record{Severity: logs.Info, Source: "s", Body: body})
	// the marker is cut instead: 4085 bytes, then 8 of «redacted:device_token», then the ellipsis
	if got := held(t, p)[0].GetBody().GetStringValue(); got != strings.Repeat("a", 4085)+"«redact…" {
		t.Fatalf("body tail = %q", got[len(got)-24:])
	}
}

// a huge line is scrubbed only as far as the kept body can need: 64 KiB, cut
// by bytes; the rune it splits is far past what is kept, which stays valid
func TestHugeBodyScrubbedWithinWindow(t *testing.T) {
	s := &logs.Scrubber{}
	s.Set("device_token", "cdd_0123456789abcdef")
	body := "a" + strings.Repeat("é", 1<<19)
	if got := logs.ScrubBody(s, body); len(got) != 64<<10 {
		t.Fatalf("scrubbed %d bytes", len(got))
	}
	p := logs.New(nil, s, discard())
	p.Emit(logs.Record{Severity: logs.Info, Source: "s", Body: body})
	if got := held(t, p)[0].GetBody().GetStringValue(); !utf8.ValidString(got) || len(got) > 4096 {
		t.Fatalf("kept %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}

// invalid bytes after a secret: the window is cut by bytes, never walked back
// over them to just before the secret's last byte
func TestSecretBeforeInvalidBytesScrubbed(t *testing.T) {
	const tok = "cdd_0123456789abcdef"
	s := &logs.Scrubber{}
	s.Set("device_token", tok)
	p := logs.New(nil, s, discard())
	p.Emit(logs.Record{Severity: logs.Info, Source: "s", Body: "xx" + tok + strings.Repeat("\x80", 70000)})
	got := held(t, p)[0].GetBody().GetStringValue()
	if !strings.HasPrefix(got, "xx«redacted:device_token»") || strings.Contains(got, tok[:8]) {
		t.Fatalf("kept %q", got[:min(len(got), 48)])
	}
}

// a long secret over and over past 64 KiB: its markers are so much shorter that
// a copy the window cut could slide into the kept body, so the window grows
func TestLongSecretPastWindowNeverHalfKept(t *testing.T) {
	var long strings.Builder
	for i := range 400 {
		fmt.Fprintf(&long, "%05d", i)
	}
	s := &logs.Scrubber{}
	s.Set("n", long.String())
	p := logs.New(nil, s, discard())
	p.Emit(logs.Record{Severity: logs.Info, Source: "s", Body: strings.Repeat(long.String(), 40)})
	if got := held(t, p)[0].GetBody().GetStringValue(); strings.ContainsAny(got, "0123456789") {
		t.Fatalf("part of the secret kept: %q", got[len(got)-64:])
	}
}

// a 33 KB secret: the product its window needs wraps a 32-bit int to under
// 64 KiB, and the window must still cover the body whole
func TestHugeSecretWindow(t *testing.T) {
	var long strings.Builder
	for i := range 6651 {
		fmt.Fprintf(&long, "%05d", i)
	}
	s := &logs.Scrubber{}
	s.Set("n", long.String())
	p := logs.New(nil, s, discard())
	p.Emit(logs.Record{Severity: logs.Info, Source: "s", Body: strings.Repeat(long.String(), 3)})
	if got := held(t, p)[0].GetBody().GetStringValue(); got != strings.Repeat("«redacted:n»", 3) {
		t.Fatalf("kept %q", got[:min(len(got), 48)])
	}
}
