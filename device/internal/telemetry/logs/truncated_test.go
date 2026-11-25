package logs_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
)

const cutToken = "cdd_0123456789abcdef"

func tokenPipeline(t *testing.T) *logs.Pipeline {
	t.Helper()
	s := &logs.Scrubber{}
	s.Set("device_token", cutToken)
	return logs.New(nil, s, discard())
}

func keptBody(t *testing.T, p *logs.Pipeline, r logs.Record) string {
	t.Helper()
	return one(t, p, r).GetBody().GetStringValue()
}

// a source cut the line inside a secret: what is left of it at the end is
// redacted, with or without the ellipsis the source put there
func TestTruncatedTailRedacted(t *testing.T) {
	p := tokenPipeline(t)
	for _, body := range []string{"x=" + cutToken[:12] + "…", "x=" + cutToken[:12]} {
		if got := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "soloist", Body: body, Truncated: true}); got != "x=«redacted:device_token»…" {
			t.Fatalf("%q kept as %q", body, got)
		}
	}
	// an encoded form cut short too
	b64 := base64.StdEncoding.EncodeToString([]byte(cutToken))
	if got := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "kmsg", Body: "auth " + b64[:9], Truncated: true}); got != "auth «redacted:device_token»…" {
		t.Fatalf("base64 kept as %q", got)
	}
	// a whole secret before the cut is scrubbed as always
	if got := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: cutToken + " then " + cutToken[:6], Truncated: true}); got != "«redacted:device_token» then «redacted:device_token»…" {
		t.Fatalf("kept %q", got)
	}
}

// fewer than 4 bytes of a secret are too likely to end a line by chance: they stay
func TestTruncatedShortTailKept(t *testing.T) {
	p := tokenPipeline(t)
	if got := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: "x=" + cutToken[:3], Truncated: true}); got != "x=cdd…" {
		t.Fatalf("kept %q", got)
	}
	if got := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: "x=" + cutToken[:4], Truncated: true}); got != "x=«redacted:device_token»…" {
		t.Fatalf("kept %q", got)
	}
}

// a whole line ends where its source ended it: a secret's first bytes there are text
func TestWholeLineTailKept(t *testing.T) {
	p := tokenPipeline(t)
	if got := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: "x=" + cutToken[:12]}); got != "x="+cutToken[:12] {
		t.Fatalf("kept %q", got)
	}
}

// the end that starts earliest is redacted, whichever secret it belongs to
func TestTruncatedLongestTail(t *testing.T) {
	s := &logs.Scrubber{}
	s.Set("a", "abcdefgh12")
	s.Set("b", "defgh1xyz")
	p := logs.New(nil, s, discard())
	if got := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: "k abcdefgh1", Truncated: true}); got != "k «redacted:a»…" {
		t.Fatalf("kept %q", got)
	}
}

// a cut line is counted; it still fits the record's own cut
func TestTruncatedCounted(t *testing.T) {
	p := tokenPipeline(t)
	p.Emit(logs.Record{Severity: logs.Info, Source: "s", Body: "whole"})
	long := strings.Repeat("a", logs.MaxLine)
	got := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: long, Truncated: true})
	if len(got) > logs.MaxLine || !strings.HasSuffix(got, "…") {
		t.Fatalf("kept %d bytes", len(got))
	}
	if st := p.Stats(); st.Cut != 1 {
		t.Fatalf("cut = %d", st.Cut)
	}
}

func TestCapLine(t *testing.T) {
	exact := strings.Repeat("a", logs.MaxLine)
	if got, cut := logs.CapLine(exact); got != exact || cut {
		t.Fatalf("exact: %d bytes, cut %v", len(got), cut)
	}
	if got, cut := logs.CapLine(exact + "b"); got != exact || !cut {
		t.Fatalf("long: %d bytes, cut %v", len(got), cut)
	}
}

// a 256-byte secret split where its source sent the first part early: neither
// its head, at the end of the first line, nor its tail, at the start of the
// second, ships
func TestSecretSplitAcrossAFlushNeverShips(t *testing.T) {
	var b strings.Builder
	for i := range 64 {
		fmt.Fprintf(&b, "k%03d", i)
	}
	secret := b.String()
	s := &logs.Scrubber{}
	s.Set("media.soloist.apiKey", secret)
	p := logs.New(nil, s, discard())
	for _, at := range []int{4, 100, 252} {
		head := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "soloist", Body: "--api-key " + secret[:at], Truncated: true})
		tail := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "soloist", Body: secret[at:] + " --ws", Continued: true})
		if head != "--api-key «redacted:media.soloist.apiKey»…" || tail != "…«redacted:media.soloist.apiKey» --ws" {
			t.Fatalf("split at %d: kept %q and %q", at, head, tail)
		}
	}
}

// a continued line starts with what may end a secret: at least 4 bytes of one
// are redacted, fewer stay; a line that is no continuation keeps its start
func TestContinuedHead(t *testing.T) {
	p := tokenPipeline(t)
	if got := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: cutToken[len(cutToken)-4:] + " ok", Continued: true}); got != "…«redacted:device_token» ok" {
		t.Fatalf("kept %q", got)
	}
	if got := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: "…" + cutToken[len(cutToken)-3:] + " ok", Continued: true}); got != "…def ok" {
		t.Fatalf("kept %q", got)
	}
	if got := keptBody(t, p, logs.Record{Severity: logs.Info, Source: "s", Body: cutToken[8:] + " ok"}); got != cutToken[8:]+" ok" {
		t.Fatalf("kept %q", got)
	}
	// the head that ends latest is redacted, whichever secret it belongs to
	s := &logs.Scrubber{}
	s.Set("a", "21hgfedcba")
	s.Set("b", "zyx1hgfed")
	q := logs.New(nil, s, discard())
	if got := keptBody(t, q, logs.Record{Severity: logs.Info, Source: "s", Body: "1hgfedcba k", Continued: true}); got != "…«redacted:a» k" {
		t.Fatalf("kept %q", got)
	}
}
