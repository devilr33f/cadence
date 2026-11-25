package telemetry

import (
	"bytes"
	"encoding/json"
	"math"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

// per-request caps on metrics (M6b-D9): points past them are rejected
const (
	maxPoints        = 2000
	maxPointAttrs    = 64
	maxAttrBytes     = 128
	maxMetricName    = 255
	partialMetricErr = "some data points were rejected"
)

// the stored kinds of metric_samples.kind
const (
	KindGauge int16 = 1
	// a monotonic cumulative sum: a reset shows as a decrease
	KindSum int16 = 2
)

// field numbers of ExportMetricsServiceRequest down to NumberDataPoint
const (
	reqResourceMetrics = 1
	rmScopeMetrics     = 2
	smMetrics          = 2
	mName              = 1
	mGauge             = 5
	mSum               = 7
	mHistogram         = 9
	mExpHistogram      = 10
	mSummary           = 11
	dataPoints         = 1
	sumTemporality     = 2
	sumMonotonic       = 3
	dpStartTime        = 2
	dpTime             = 3
	dpAsDouble         = 4
	dpAsInt            = 6
	dpAttributes       = 7
	dpFlags            = 8
	// AGGREGATION_TEMPORALITY_CUMULATIVE: a sum is stored as its running value
	cumulative = 2
	// DATA_POINT_FLAGS_NO_RECORDED_VALUE_MASK
	noRecordedValue = 1
)

// metric is what the server reads of an OTLP Metric; name and data point into the request.
type metric struct {
	name []byte
	form protowire.Number // the data oneof's member: mGauge, mSum, … or 0
	data []byte
}

// point is a stored data point: its attributes as a JSON object; start is
// the start time of a sum's point, 0 when unset.
type point struct {
	time  uint64
	start uint64
	value float64
	attrs []byte
}

// eachMetric calls fn with every metric of an ExportMetricsServiceRequest, in order.
func eachMetric(req []byte, fn func(m *metric) error) error {
	return each(req, reqResourceMetrics, func(rm []byte) error {
		return each(rm, rmScopeMetrics, func(sm []byte) error {
			return each(sm, smMetrics, func(b []byte) error {
				var m metric
				err := walk(b, func(f field) error {
					if f.typ != protowire.BytesType {
						return nil
					}
					switch f.num {
					case mName:
						m.name = f.b
					case mGauge, mSum, mHistogram, mExpHistogram, mSummary:
						// the oneof's last member wins
						m.form, m.data = f.num, f.b
					}
					return nil
				})
				if err != nil {
					return err
				}
				return fn(&m)
			})
		})
	})
}

// kind is the stored kind of m's points, 0 when they cannot be stored at
// all: a gauge, or a cumulative sum (a gauge when it is not monotonic), of a
// name of 1 to 255 bytes of valid UTF-8 without NUL. A sum's start time is not
// checked against its points' times: a device's may lie past them, as its
// clock was set after it started.
func (m *metric) kind() (int16, error) {
	n := m.name
	if len(n) == 0 || len(n) > maxMetricName || !utf8.Valid(n) || hasNUL(n) {
		return 0, nil
	}
	switch m.form {
	case mGauge:
		return KindGauge, nil
	case mSum:
		var temporality, monotonic uint64
		err := walk(m.data, func(f field) error {
			switch {
			case f.num == sumTemporality && f.typ == protowire.VarintType:
				temporality = f.u
			case f.num == sumMonotonic && f.typ == protowire.VarintType:
				monotonic = f.u
			}
			return nil
		})
		switch {
		case err != nil || temporality != cumulative:
			return 0, err
		case monotonic != 0:
			return KindSum, nil
		}
		return KindGauge, nil
	}
	return 0, nil
}

// eachPoint calls fn with each data point of m, of whatever kind: gauges, sums,
// histograms and summaries all list them as field 1.
func (m *metric) eachPoint(fn func(b []byte) error) error {
	if m.form == 0 {
		return nil
	}
	return each(m.data, dataPoints, fn)
}

// parsePoint reads a NumberDataPoint; ok is false for one that cannot be
// stored: no value, a value that is not finite, or attributes past the caps.
func parsePoint(b []byte) (p point, ok bool, err error) {
	var (
		has   bool
		flags uint64
		attrs map[string]any
		count int
		bad   bool
	)
	err = walk(b, func(f field) error {
		switch {
		case f.num == dpTime && f.typ == protowire.Fixed64Type:
			p.time = f.u
		case f.num == dpStartTime && f.typ == protowire.Fixed64Type:
			p.start = f.u
		case f.num == dpAsDouble && f.typ == protowire.Fixed64Type:
			p.value, has = math.Float64frombits(f.u), true
		case f.num == dpAsInt && f.typ == protowire.Fixed64Type:
			p.value, has = float64(int64(f.u)), true //nolint:gosec // sfixed64 is two's complement
		case f.num == dpFlags && f.typ == protowire.VarintType:
			flags = f.u
		case f.num == dpAttributes && f.typ == protowire.BytesType:
			if count++; bad || count > maxPointAttrs {
				bad = true
				return nil
			}
			key, v, perr := parseKeyValue(f.b)
			if perr != nil {
				return perr
			}
			val, vok := attrValue(v)
			if !vok || len(key) == 0 || !text(key) {
				bad = true
				return nil
			}
			if attrs == nil {
				attrs = map[string]any{}
			}
			attrs[string(key)] = val
		}
		return nil
	})
	if err != nil || bad || !has || flags&noRecordedValue != 0 || math.IsNaN(p.value) || math.IsInf(p.value, 0) {
		return point{}, false, err
	}
	if attrs == nil {
		p.attrs = []byte("{}")
		return p, true, nil
	}
	p.attrs, err = attrsJSON(attrs)
	return p, err == nil, err
}

// attrsJSON is attrs as a JSON object, its strings as written: json.Marshal
// would escape <, > and & for HTML, which no reader of it is.
func attrsJSON(attrs map[string]any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(attrs); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// text is an attribute key or string value as stored: at most 128 bytes of
// valid UTF-8 without C0 control bytes (jsonb holds no NUL, and none of the
// others is a name or a value of a device's).
func text(b []byte) bool {
	if len(b) > maxAttrBytes || !utf8.Valid(b) {
		return false
	}
	for _, c := range b {
		if c < 0x20 {
			return false
		}
	}
	return true
}

// attrValue is a scalar attribute's value as JSON takes it.
func attrValue(v anyValue) (any, bool) {
	switch v.kind {
	case kindString:
		return string(v.s), text(v.s)
	case kindBool:
		return v.u != 0, true
	case kindInt:
		return int64(v.u), true //nolint:gosec // an int64 on the wire
	case kindDouble:
		d := v.double()
		return d, !math.IsNaN(d) && !math.IsInf(d, 0)
	}
	return nil, false
}
