package settings

import (
	"bytes"
	"encoding/json"
)

// FollowUp is when the server offers a follow-up after a spoken reply
// (M6a-D5): the window the device listens for, and at most Limit follow-ups
// in a row after a wake or press.
type FollowUp struct {
	Enabled  bool `json:"enabled"`
	WindowMs int  `json:"windowMs"`
	Limit    int  `json:"limit"`
}

const (
	minFollowUpWindowMs = 2000
	maxFollowUpWindowMs = 15000
	maxFollowUpLimit    = 10
)

// validFollowUp checks a followUp value: an object of exactly enabled,
// windowMs and limit, within their bounds.
func validFollowUp(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 {
		return false
	}
	for _, k := range []string{"enabled", "windowMs", "limit"} {
		if v, ok := fields[k]; !ok || isNull(v) {
			return false
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f FollowUp
	if dec.Decode(&f) != nil {
		return false
	}
	return f.WindowMs >= minFollowUpWindowMs && f.WindowMs <= maxFollowUpWindowMs && f.Limit >= 1 && f.Limit <= maxFollowUpLimit
}
