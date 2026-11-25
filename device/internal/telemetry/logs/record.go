// Package logs is the device's log pipeline: every record is filtered by its
// source's level, scrubbed of known secrets, kept in a bounded ring and
// shipped to the server over OTLP.
package logs

import (
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	logsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/logs/v1"
)

// Severity is the OTLP severity number of the four levels a device logs at.
type Severity int32

const (
	Debug Severity = 5
	Info  Severity = 9
	Warn  Severity = 13
	Error Severity = 17
)

const (
	maxBody  = 4096
	ellipsis = "…"
)

// MaxLine is the longest line a source hands Emit, so the scrub before the
// record's own cut never runs over an unbounded one.
const MaxLine = maxBody

// CapLine cuts s to MaxLine bytes, by bytes: the cut is the record's
// Truncated, so whatever it splits is redacted or made valid later.
func CapLine(s string) (line string, cut bool) {
	if len(s) <= MaxLine {
		return s, false
	}
	return s[:MaxLine], true
}

// level is one of the four levels, a number between them taken as the one below.
func (s Severity) level() Severity {
	switch {
	case s >= Error:
		return Error
	case s >= Warn:
		return Warn
	case s >= Info:
		return Info
	}
	return Debug
}

func (s Severity) text() string {
	switch s.level() {
	case Error:
		return "ERROR"
	case Warn:
		return "WARN"
	case Info:
		return "INFO"
	}
	return "DEBUG"
}

// Record is one log line of a source: cadenced, audiod, one of audiod's
// children, kmsg or a board file.
type Record struct {
	Time     time.Time
	Severity Severity
	Source   string
	Body     string
	// the source cut Body at its end (and may have ended it with …): the cut
	// can have left the first bytes of a secret there, which Emit redacts
	Truncated bool
	// Body is the rest of a line its source cut before (and may start with …): its
	// start can hold the last bytes of a secret, which Emit redacts
	Continued bool
}

// encode returns r as the wire bytes of an OTLP LogRecord, its body valid
// UTF-8 and at most 4096 bytes; a zero time is now. The body is scrubbed by
// then: the cut must never keep part of a secret. A cut body, the source's or
// this one, carries the bool attribute truncated, and the rest of a cut line
// continued, so the server keeps both and redacts the ends again.
func (r Record) encode(now time.Time) ([]byte, error) {
	if r.Time.IsZero() {
		r.Time = now
	}
	sev := r.Severity.level()
	body := strings.ToValidUTF8(r.Body, "�")
	attrs := []*commonv1.KeyValue{{Key: "source", Value: str(strings.ToValidUTF8(r.Source, "�"))}}
	if r.Truncated || len(body) > maxBody {
		attrs = append(attrs, &commonv1.KeyValue{Key: "truncated", Value: boolean(true)})
	}
	if r.Continued {
		attrs = append(attrs, &commonv1.KeyValue{Key: "continued", Value: boolean(true)})
	}
	return proto.Marshal(&logsv1.LogRecord{
		TimeUnixNano:   uint64(r.Time.UnixNano()), //nolint:gosec // a time before 1970 is no device's clock
		SeverityNumber: logsv1.SeverityNumber(sev),
		SeverityText:   sev.text(),
		Body:           str(cut(body)),
		Attributes:     attrs,
	})
}

func str(s string) *commonv1.AnyValue {
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: s}}
}

func boolean(b bool) *commonv1.AnyValue {
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_BoolValue{BoolValue: b}}
}

// cut keeps s whole up to maxBody bytes, else as much of it as leaves room for
// the ellipsis, on a rune boundary.
func cut(s string) string {
	if len(s) <= maxBody {
		return s
	}
	return cutAt(s, maxBody-len(ellipsis)) + ellipsis
}

// cutAt keeps at most n bytes of s, on a rune boundary when s is valid UTF-8:
// it steps back over at most UTFMax-1 bytes, never over a run of invalid ones.
func cutAt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for i := 0; i < utf8.UTFMax-1 && n > 0 && !utf8.RuneStart(s[n]); i++ {
		n--
	}
	return s[:n]
}
