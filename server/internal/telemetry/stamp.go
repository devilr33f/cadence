// Package telemetry takes the devices' logs and metrics over OTLP/HTTP and
// keeps them, with the server's own log lines, in the store (M6b-D8, D9).
package telemetry

import (
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// the longest body kept, as the device cuts its own
	maxBody  = 4096
	ellipsis = "…"
	// a time further than this from the receive time is a wrong clock (a
	// device at 1970), so the receive time stands in for it. a log record may
	// wait in the device's ring through an outage; a metric point is sent at
	// once, and a late one would be drawn out of place in its series
	logSkew    = 24 * time.Hour
	metricSkew = 5 * time.Minute
)

// recordTime is the time a record or point is stored at: its own (else its
// observed one, for a log record), or received when that is unset or more
// than skew off received, so no partition is ever made for an absurd day.
func recordTime(own, observed uint64, received time.Time, skew time.Duration) time.Time {
	n := own
	if n == 0 {
		n = observed
	}
	if n == 0 || n > uint64(1<<63-1) {
		return received
	}
	t := time.Unix(0, int64(n)) //nolint:gosec // checked against the int64 range above
	if t.Before(received.Add(-skew)) || t.After(received.Add(skew)) {
		return received
	}
	return t
}

// keepBody is what the store keeps of a device's log body: scrubbed as Body
// does, valid UTF-8 without NUL (postgres text holds neither), and at most
// maxBody bytes, a longer one cut on a rune boundary and ended with the
// ellipsis; cut reports that the stored body is cut, by the device or here.
func keepBody(s *Scrubber, body string, truncated, continued bool) (kept string, cut bool) {
	kept, cut = cutBody(clean(s.Body(body, truncated, continued)))
	return kept, cut || truncated
}

// NormalizeExternalLog applies the same clock, scrubbing and size rules to a
// log received from a source other than cadenced. Observed is the receive time.
func NormalizeExternalLog(scrub *Scrubber, row LogRow) LogRow {
	var own uint64
	if !row.Time.IsZero() && !row.Time.Before(time.Unix(0, 0)) {
		own = uint64(row.Time.UnixNano()) //nolint:gosec // checked after 1970
	}
	row.Time = recordTime(own, 0, row.Observed, logSkew)
	row.Body, row.Truncated = keepBody(scrub, row.Body, row.Truncated, row.Continued)
	return row
}

// clean makes s valid UTF-8 without NUL, each bad byte or NUL replaced by U+FFFD.
func clean(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "�")
}

// cutBody keeps s whole up to maxBody bytes, else as much as leaves room for
// the ellipsis, on a rune boundary.
func cutBody(s string) (string, bool) {
	if len(s) <= maxBody {
		return s, false
	}
	n := maxBody - len(ellipsis)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + ellipsis, true
}
