package settings

import (
	"bytes"
	"encoding/json"
)

// Tracing is the server's tracing (internal/tracing), applied without a
// restart: whether spans are recorded at all, the share of new traces
// sampled (a trace a device or caller started follows its parent's
// decision), and how many finished traces the store keeps.
type Tracing struct {
	Enabled     bool    `json:"enabled"`
	SampleRatio float64 `json:"sampleRatio" minimum:"0" maximum:"1"`
	RingSize    int     `json:"ringSize" minimum:"10" maximum:"5000"`
}

const (
	minTraceRing = 10
	maxTraceRing = 5000
)

// validTracing checks a tracing value: an object of exactly enabled,
// sampleRatio and ringSize, within their bounds.
func validTracing(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 {
		return false
	}
	for _, k := range []string{"enabled", "sampleRatio", "ringSize"} {
		if v, ok := fields[k]; !ok || isNull(v) {
			return false
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var t Tracing
	if dec.Decode(&t) != nil {
		return false
	}
	return t.SampleRatio >= 0 && t.SampleRatio <= 1 && t.RingSize >= minTraceRing && t.RingSize <= maxTraceRing
}
