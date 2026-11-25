package telemetry

import (
	"bytes"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// log records stored from one request; the device sends at most 4 MiB of
	// records of 40 bytes or more, so this bounds what a hostile one makes the
	// server hold, not what a device sends
	maxLogRecords = 128 << 10
	maxSource     = 64
	// the source of the server's own lines: a device cannot claim it
	serverSource = "server"
	maxSeverity  = 24
)

// field numbers of ExportLogsServiceRequest, ResourceLogs, ScopeLogs and LogRecord
const (
	reqResourceLogs  = 1
	rlScopeLogs      = 2
	slLogRecords     = 2
	lrTime           = 1
	lrSeverity       = 2
	lrBody           = 5
	lrAttributes     = 6
	lrObservedTime   = 11
	attrSource       = "source"
	attrTruncated    = "truncated"
	attrContinued    = "continued"
	partialLogsError = "some log records were rejected"
)

// logRecord is what the server reads of an OTLP LogRecord; body and source
// point into the request. The device's other attributes, its resource and its
// scope are never read: the server stamps the device itself.
type logRecord struct {
	time, observed uint64
	severity       uint64
	body           anyValue
	source         anyValue
	truncated      bool
	continued      bool
}

// eachLogRecord calls fn with every log record of an ExportLogsServiceRequest, in order.
func eachLogRecord(req []byte, fn func(r *logRecord)) error {
	return each(req, reqResourceLogs, func(rl []byte) error {
		return each(rl, rlScopeLogs, func(sl []byte) error {
			return each(sl, slLogRecords, func(b []byte) error {
				var r logRecord
				if err := parseLogRecord(b, &r); err != nil {
					return err
				}
				fn(&r)
				return nil
			})
		})
	})
}

func parseLogRecord(b []byte, r *logRecord) error {
	return walk(b, func(f field) error {
		switch {
		case f.num == lrTime && f.typ == protowire.Fixed64Type:
			r.time = f.u
		case f.num == lrObservedTime && f.typ == protowire.Fixed64Type:
			r.observed = f.u
		case f.num == lrSeverity && f.typ == protowire.VarintType:
			r.severity = f.u
		case f.num == lrBody && f.typ == protowire.BytesType:
			v, err := parseAny(f.b)
			r.body = v
			return err
		case f.num == lrAttributes && f.typ == protowire.BytesType:
			key, v, err := parseKeyValue(f.b)
			switch string(key) {
			case attrSource:
				r.source = v
			case attrTruncated:
				r.truncated = v.kind == kindBool && v.u != 0
			case attrContinued:
				r.continued = v.kind == kindBool && v.u != 0
			}
			return err
		}
		return nil
	})
}

// valid reports whether r can be stored: a string body, a severity number of
// OTLP's and a source of 1 to 64 bytes of valid UTF-8 without NUL that is not
// the server's own.
func (r *logRecord) valid() bool {
	if r.body.kind != kindString || r.severity > maxSeverity || r.source.kind != kindString {
		return false
	}
	s := r.source.s
	return len(s) > 0 && len(s) <= maxSource && utf8.Valid(s) && !hasNUL(s) && string(s) != serverSource
}

func hasNUL(b []byte) bool { return bytes.IndexByte(b, 0) >= 0 }
