package logs_test

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
)

// a value with characters every escaping and encoding changes: 34 bytes, so the
// padded and raw base64 forms differ, and its standard base64 holds both + and /
const secret = "s3cr3t/key+with spaces&=?é~~~???x"

func TestScrubEveryForm(t *testing.T) {
	var s logs.Scrubber
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
	var s logs.Scrubber
	s.Set("inner", "abcdefgh")
	s.Set("outer", "xx-abcdefgh-yy")
	if got := s.Scrub("a xx-abcdefgh-yy b abcdefgh c"); got != "a «redacted:outer» b «redacted:inner» c" {
		t.Fatalf("scrubbed = %q", got)
	}
	// the same with the names the other way round, whatever the order they were set in
	var r logs.Scrubber
	r.Set("outer", "abcdefgh")
	r.Set("inner", "xx-abcdefgh-yy")
	if got := r.Scrub("a xx-abcdefgh-yy b"); got != "a «redacted:inner» b" {
		t.Fatalf("scrubbed = %q", got)
	}
}

func TestScrubIgnoresShortValues(t *testing.T) {
	var s logs.Scrubber
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
	var s logs.Scrubber
	s.Set("a.key", "first-value-1")
	s.Set("a.key", "second-value-2")
	if got := s.Scrub("first-value-1 second-value-2"); got != "first-value-1 «redacted:a.key»" {
		t.Fatalf("after replace = %q", got)
	}
	s.Remove("a.key")
	if got := s.Scrub("second-value-2"); got != "second-value-2" {
		t.Fatalf("after remove = %q", got)
	}
	// a short value in place of a long one leaves nothing to match
	s.Set("b.key", "long-enough-value")
	s.Set("b.key", "short")
	if got := s.Scrub("long-enough-value short"); got != "long-enough-value short" {
		t.Fatalf("after short replace = %q", got)
	}
	// a name may hold several values: a refused config's beside the one in force
	s.Set("c.key", "in-force-value", "refused-value")
	if got := s.Scrub("in-force-value refused-value"); got != "«redacted:c.key» «redacted:c.key»" {
		t.Fatalf("two values = %q", got)
	}
	var zero logs.Scrubber
	if got := zero.Scrub("untouched"); got != "untouched" {
		t.Fatalf("zero scrubber = %q", got)
	}
}

func TestScrubConcurrentUse(t *testing.T) {
	var s logs.Scrubber
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
