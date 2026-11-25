package telemetry

import (
	"cmp"
	"encoding/base64"
	"maps"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	// a shorter value would match too much text by chance; none of ours is that short
	minSecret = 8
	// a body is cut here before it is scrubbed, so a huge line costs no more than this
	scrubWindow = 64 << 10
	// the shortest end of a cut line taken for a secret's start
	minTail = 4
)

// Scrubber replaces every known secret value in a text by «redacted:<name>»:
// the value as it is, URL-escaped (query and path) and base64-encoded
// (standard and URL alphabets, padded or not). The zero value knows none; it
// is safe for concurrent use. It is the server's copy of the device's
// scrubber (device/internal/telemetry/logs), with the same forms and rules:
// the server scrubs every device log body again (M6b-D3).
//
// It matches whole values in one text, at the end of a text its source cut
// (a record marked truncated) the first 4 bytes or more of one, and at the
// start of the rest of a cut line (marked continued) the last 4 bytes or more
// of one, so a secret cut once is stored as neither half. A segment cut at both ends inside one
// secret (a line flushed twice while the secret was written, two pauses of a
// second or more within it) holds neither the secret's start nor its end and
// ships as it is; so does a secret split across two lines its source ended, or
// one nested in a larger encoding (base64 of "user:pass" or of
// "Bearer <token>", a JSON-escaped value).
type Scrubber struct {
	mu     sync.Mutex
	values map[string][]string
	set    atomic.Pointer[patterns]
}

type patterns struct {
	// longest first, so a secret that holds another is replaced whole
	list []pattern
	// how much of a body is scrubbed before the cut (see window)
	window int
}

type pattern struct{ text, marker string }

// Set makes values the secrets named name, replacing those it had; a value
// under 8 bytes is left out.
func (s *Scrubber) Set(name string, values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string][]string{}
	}
	s.values[name] = slices.Clone(values)
	s.rebuild()
}

func (s *Scrubber) Remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, name)
	s.rebuild()
}

func (s *Scrubber) Scrub(text string) string {
	ps := s.set.Load()
	if ps == nil {
		return text
	}
	for _, p := range ps.list {
		if strings.Contains(text, p.text) {
			text = strings.ReplaceAll(text, p.text, p.marker)
		}
	}
	return text
}

// Body is what is kept of a log body before its cut to maxBody bytes: its
// window scrubbed, and the end of a truncated one and the start of a continued
// one redacted where they begin or end a known secret's form, as the device
// does before it ships the record.
func (s *Scrubber) Body(body string, truncated, continued bool) string {
	body = s.scrubBody(body)
	if truncated {
		body = s.scrubTail(strings.TrimSuffix(body, ellipsis)) + ellipsis
	}
	if continued {
		body = ellipsis + s.scrubHead(strings.TrimPrefix(body, ellipsis))
	}
	return body
}

// scrubBody scrubs at most the window of body's first bytes: the body is cut
// to maxBody afterwards, and no part of a secret the window cut can reach it.
// The window is cut by bytes: walking back to a rune start over invalid bytes
// could stop just before a secret's last byte, and a rune it splits is made
// valid later, far past what is kept.
func (s *Scrubber) scrubBody(body string) string {
	window := scrubWindow
	if ps := s.set.Load(); ps != nil {
		window = ps.window
	}
	if len(body) > window {
		body = body[:window]
	}
	return s.Scrub(body)
}

// scrubTail redacts the end of a cut text where it begins a known secret's
// form: the cut may have kept that secret's first bytes, which no whole
// pattern matches. The end that starts earliest wins; fewer than minTail bytes
// stay, as too short an end begins a secret by chance.
func (s *Scrubber) scrubTail(text string) string {
	ps := s.set.Load()
	if ps == nil {
		return text
	}
	best, marker := len(text), ""
	for _, p := range ps.list {
		// shorter than the form: a whole one was scrubbed already
		for k := min(len(p.text)-1, len(text)); k >= minTail; k-- {
			start := len(text) - k
			if start >= best {
				break
			}
			if text[start] == p.text[0] && text[start:] == p.text[:k] {
				best, marker = start, p.marker
				break
			}
		}
	}
	if marker == "" {
		return text
	}
	return text[:best] + marker
}

// scrubHead redacts the start of the rest of a cut line where it ends a known
// secret's form, as scrubTail does the end of the part before: the start that
// ends latest wins; fewer than minTail bytes stay.
func (s *Scrubber) scrubHead(text string) string {
	ps := s.set.Load()
	if ps == nil {
		return text
	}
	best, marker := 0, ""
	for _, p := range ps.list {
		// shorter than the form: a whole one was scrubbed already
		for k := min(len(p.text)-1, len(text)); k >= minTail && k > best; k-- {
			if text[k-1] == p.text[len(p.text)-1] && text[:k] == p.text[len(p.text)-k:] {
				best, marker = k, p.marker
				break
			}
		}
	}
	if marker == "" {
		return text
	}
	return marker + text[best:]
}

// rebuild runs under mu
func (s *Scrubber) rebuild() {
	seen := map[string]bool{}
	var ps patterns
	longest, shortestMarker := 0, 0
	for _, name := range slices.Sorted(maps.Keys(s.values)) {
		marker := "«redacted:" + name + "»"
		for _, v := range s.values[name] {
			if len(v) < minSecret {
				continue
			}
			for _, f := range forms(v) {
				// a value two names share is redacted under the first
				if seen[f] {
					continue
				}
				seen[f] = true
				ps.list = append(ps.list, pattern{text: f, marker: marker})
				longest = max(longest, len(f))
				if shortestMarker == 0 || len(marker) < shortestMarker {
					shortestMarker = len(marker)
				}
			}
		}
	}
	slices.SortStableFunc(ps.list, func(a, b pattern) int { return cmp.Compare(len(b.text), len(a.text)) })
	ps.window = scrubWindow
	if longest > 0 {
		// a secret the window cuts starts within its last longest bytes; what is before it
		// shrinks at worst by shortestMarker/longest when it is all secrets, and must still
		// fill the kept body
		// in int64, as int is 32 bits on midi: a secret comes in one frame or the config file, so
		// longest is far below 2^31 and the product fits; a window past any body is as good as the
		// largest int32
		l, m := int64(longest), int64(shortestMarker)
		ps.window = int(min(max(scrubWindow, l+(maxBody+l)*l/m+1), math.MaxInt32))
	}
	s.set.Store(&ps)
}

func forms(v string) []string {
	b := []byte(v)
	return []string{
		v, url.QueryEscape(v), url.PathEscape(v),
		base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
		base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
	}
}
