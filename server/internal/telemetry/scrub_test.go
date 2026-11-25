package telemetry_test

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// the device scrubber's tests (device/internal/telemetry/logs), on the server's copy

// a value with characters every escaping and encoding changes: 34 bytes, so the
// padded and raw base64 forms differ, and its standard base64 holds both + and /
const secret = "s3cr3t/key+with spaces&=?é~~~???x"

const cutToken = "cdd_0123456789abcdef"

func TestScrubEveryForm(t *testing.T) {
	var s telemetry.Scrubber
	s.Set("media.soloist.key", secret)
	b := []byte(secret)
	forms := map[string]string{
		"plain":           secret,
		"query escaped":   url.QueryEscape(secret),
		"path escaped":    url.PathEscape(secret),
		"std base64":      base64.StdEncoding.EncodeToString(b),
		"std base64 raw":  base64.RawStdEncoding.EncodeToString(b),
		"url base64":      base64.URLEncoding.EncodeToString(b),
		"url base64 raw":  base64.RawURLEncoding.EncodeToString(b),
		"twice in a line": secret + " and " + secret,
	}
	// otherwise one base64 case would stand in for another
	b64 := []string{forms["std base64"], forms["std base64 raw"], forms["url base64"], forms["url base64 raw"]}
	for i := range b64 {
		for j := range i {
			if b64[i] == b64[j] {
				t.Fatalf("base64 forms %d and %d are the same: %q", i, j, b64[i])
			}
		}
	}
	if std := forms["std base64"]; !strings.Contains(std, "+") || !strings.Contains(std, "/") {
		t.Fatalf("std base64 %q lacks + or /", std)
	}
	for name, form := range forms {
		t.Run(name, func(t *testing.T) {
			got := s.Scrub("before " + form + " after")
			if strings.Contains(got, form) {
				t.Fatalf("not scrubbed: %q", got)
			}
			if !strings.HasPrefix(got, "before «redacted:media.soloist.key»") || !strings.HasSuffix(got, " after") {
				t.Fatalf("scrubbed = %q", got)
			}
		})
	}
}

// the outer secret is redacted whole, not cut around the inner one
func TestScrubSecretContainingAnother(t *testing.T) {
	var s telemetry.Scrubber
	s.Set("inner", "abcdefgh")
	s.Set("outer", "xx-abcdefgh-yy")
	if got := s.Scrub("a xx-abcdefgh-yy b abcdefgh c"); got != "a «redacted:outer» b «redacted:inner» c" {
		t.Fatalf("scrubbed = %q", got)
	}
	var r telemetry.Scrubber
	r.Set("outer", "abcdefgh")
	r.Set("inner", "xx-abcdefgh-yy")
	if got := r.Scrub("a xx-abcdefgh-yy b"); got != "a «redacted:inner» b" {
		t.Fatalf("scrubbed = %q", got)
	}
}

func TestScrubIgnoresShortValues(t *testing.T) {
	var s telemetry.Scrubber
	s.Set("short", "1234567")
	if got := s.Scrub("pin 1234567"); got != "pin 1234567" {
		t.Fatalf("scrubbed = %q", got)
	}
	s.Set("eight", "12345678")
	if got := s.Scrub("pin 12345678"); got != "pin «redacted:eight»" {
		t.Fatalf("scrubbed = %q", got)
	}
}

func TestScrubRemoveAndReplace(t *testing.T) {
	var s telemetry.Scrubber
	s.Set("a.key", "first-value-1")
	s.Set("a.key", "second-value-2")
	if got := s.Scrub("first-value-1 second-value-2"); got != "first-value-1 «redacted:a.key»" {
		t.Fatalf("after replace = %q", got)
	}
	s.Remove("a.key")
	if got := s.Scrub("second-value-2"); got != "second-value-2" {
		t.Fatalf("after remove = %q", got)
	}
	s.Set("b.key", "long-enough-value")
	s.Set("b.key", "short")
	if got := s.Scrub("long-enough-value short"); got != "long-enough-value short" {
		t.Fatalf("after short replace = %q", got)
	}
	s.Set("c.key", "in-force-value", "refused-value")
	if got := s.Scrub("in-force-value refused-value"); got != "«redacted:c.key» «redacted:c.key»" {
		t.Fatalf("two values = %q", got)
	}
	var zero telemetry.Scrubber
	if got := zero.Scrub("untouched"); got != "untouched" {
		t.Fatalf("zero scrubber = %q", got)
	}
}

func TestScrubConcurrentUse(t *testing.T) {
	var s telemetry.Scrubber
	var wg sync.WaitGroup
	for i := range 8 {
		name, value := fmt.Sprintf("cap.secret%d", i), fmt.Sprintf("value-of-secret-%d", i)
		wg.Go(func() {
			for range 200 {
				s.Set(name, value)
				if got := s.Scrub("x " + value + " y"); strings.Contains(got, value) {
					t.Errorf("not scrubbed while set: %q", got)
					return
				}
				s.Remove(name)
			}
		})
		wg.Go(func() {
			for range 200 {
				_ = s.Scrub("line with value-of-secret-0 and value-of-secret-7")
			}
		})
	}
	wg.Wait()
}

func tokenScrubber() *telemetry.Scrubber {
	s := &telemetry.Scrubber{}
	s.Set("device_token", cutToken)
	return s
}

// kept is what the store keeps of a body: scrubbed, valid, cut
func kept(s *telemetry.Scrubber, body string, truncated, continued bool) string {
	b, _ := telemetry.KeepBody(s, body, truncated, continued)
	return b
}

// a source cut the line inside a secret: what is left of it at the end is
// redacted, with or without the ellipsis the source put there
func TestTruncatedTailRedacted(t *testing.T) {
	s := tokenScrubber()
	for _, body := range []string{"x=" + cutToken[:12] + "…", "x=" + cutToken[:12]} {
		if got := kept(s, body, true, false); got != "x=«redacted:device_token»…" {
			t.Fatalf("%q kept as %q", body, got)
		}
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(cutToken))
	if got := kept(s, "auth "+b64[:9], true, false); got != "auth «redacted:device_token»…" {
		t.Fatalf("base64 kept as %q", got)
	}
	if got := kept(s, cutToken+" then "+cutToken[:6], true, false); got != "«redacted:device_token» then «redacted:device_token»…" {
		t.Fatalf("kept %q", got)
	}
}

// fewer than 4 bytes of a secret are too likely to end a line by chance: they stay
func TestTruncatedShortTailKept(t *testing.T) {
	s := tokenScrubber()
	if got := kept(s, "x="+cutToken[:3], true, false); got != "x=cdd…" {
		t.Fatalf("kept %q", got)
	}
	if got := kept(s, "x="+cutToken[:4], true, false); got != "x=«redacted:device_token»…" {
		t.Fatalf("kept %q", got)
	}
}

// a whole line ends where its source ended it: a secret's first bytes there are text
func TestWholeLineTailKept(t *testing.T) {
	if got := kept(tokenScrubber(), "x="+cutToken[:12], false, false); got != "x="+cutToken[:12] {
		t.Fatalf("kept %q", got)
	}
}

// the end that starts earliest is redacted, whichever secret it belongs to
func TestTruncatedLongestTail(t *testing.T) {
	s := &telemetry.Scrubber{}
	s.Set("a", "abcdefgh12")
	s.Set("b", "defgh1xyz")
	if got := kept(s, "k abcdefgh1", true, false); got != "k «redacted:a»…" {
		t.Fatalf("kept %q", got)
	}
}

// a 256-byte secret split where its source sent the first part early: neither
// its head, at the end of the first line, nor its tail, at the start of the
// second, is kept
func TestSecretSplitAcrossAFlushNeverKept(t *testing.T) {
	var b strings.Builder
	for i := range 64 {
		fmt.Fprintf(&b, "k%03d", i)
	}
	secret := b.String()
	s := &telemetry.Scrubber{}
	s.Set("media.soloist.apiKey", secret)
	for _, at := range []int{4, 100, 252} {
		head := kept(s, "--api-key "+secret[:at], true, false)
		tail := kept(s, secret[at:]+" --ws", false, true)
		if head != "--api-key «redacted:media.soloist.apiKey»…" || tail != "…«redacted:media.soloist.apiKey» --ws" {
			t.Fatalf("split at %d: kept %q and %q", at, head, tail)
		}
	}
}

// a continued line starts with what may end a secret: at least 4 bytes of one
// are redacted, fewer stay; a line that is no continuation keeps its start
func TestContinuedHead(t *testing.T) {
	s := tokenScrubber()
	if got := kept(s, cutToken[len(cutToken)-4:]+" ok", false, true); got != "…«redacted:device_token» ok" {
		t.Fatalf("kept %q", got)
	}
	if got := kept(s, "…"+cutToken[len(cutToken)-3:]+" ok", false, true); got != "…def ok" {
		t.Fatalf("kept %q", got)
	}
	if got := kept(s, cutToken[8:]+" ok", false, false); got != cutToken[8:]+" ok" {
		t.Fatalf("kept %q", got)
	}
	r := &telemetry.Scrubber{}
	r.Set("a", "21hgfedcba")
	r.Set("b", "zyx1hgfed")
	if got := kept(r, "1hgfedcba k", false, true); got != "…«redacted:a» k" {
		t.Fatalf("kept %q", got)
	}
}

// a secret is redacted before the body is cut, so one across the cut is never half kept
func TestScrubbedBeforeCut(t *testing.T) {
	body, cut := telemetry.KeepBody(tokenScrubber(), strings.Repeat("a", 4085)+cutToken, false, false)
	if body != strings.Repeat("a", 4085)+"«redact…" || !cut {
		t.Fatalf("body tail = %q, cut %v", body[len(body)-24:], cut)
	}
}

func TestBodyCutValidAndWithoutNUL(t *testing.T) {
	var s telemetry.Scrubber
	exact := strings.Repeat("a", 4096)
	if got, cut := telemetry.KeepBody(&s, exact, false, false); got != exact || cut {
		t.Fatalf("4096 bytes cut to %d (%v)", len(got), cut)
	}
	wide, cut := telemetry.KeepBody(&s, strings.Repeat("é", 3000), false, false)
	if len(wide) > 4096 || !utf8.ValidString(wide) || !strings.HasSuffix(wide, "é…") || !cut {
		t.Fatalf("cut = %d bytes, valid %v", len(wide), utf8.ValidString(wide))
	}
	// postgres text holds neither invalid bytes nor NUL
	if got, _ := telemetry.KeepBody(&s, "ok\xff\x00ok", false, false); got != "ok��ok" {
		t.Fatalf("body = %q", got)
	}
}

// a huge line is scrubbed only as far as the kept body can need: 64 KiB, cut
// by bytes; the rune it splits is far past what is kept, which stays valid
func TestHugeBodyScrubbedWithinWindow(t *testing.T) {
	s := tokenScrubber()
	body := "a" + strings.Repeat("é", 1<<19)
	if got := s.Body(body, false, false); len(got) != 64<<10 {
		t.Fatalf("scrubbed %d bytes", len(got))
	}
	if got := kept(s, body, false, false); !utf8.ValidString(got) || len(got) > 4096 {
		t.Fatalf("kept %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}

// invalid bytes after a secret: the window is cut by bytes, never walked back
// over them to just before the secret's last byte
func TestSecretBeforeInvalidBytesScrubbed(t *testing.T) {
	got := kept(tokenScrubber(), "xx"+cutToken+strings.Repeat("\x80", 70000), false, false)
	if !strings.HasPrefix(got, "xx«redacted:device_token»") || strings.Contains(got, cutToken[:8]) {
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
	s := &telemetry.Scrubber{}
	s.Set("n", long.String())
	if got := kept(s, strings.Repeat(long.String(), 40), false, false); strings.ContainsAny(got, "0123456789") {
		t.Fatalf("part of the secret kept: %q", got[len(got)-64:])
	}
}
