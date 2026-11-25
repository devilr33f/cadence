package settings

import (
	"bytes"
	"encoding/json"
	"regexp"
	"time"
)

// QuietHours hold back what the server would say on a device unasked (task
// updates): from Start to End, "HH:MM" on the device's timeZone clock; a range
// whose Start is after its End wraps past midnight.
type QuietHours struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

var clockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// minuteOf is the minute of the day a "HH:MM" names.
func minuteOf(hhmm string) (int, bool) {
	if !clockPattern.MatchString(hhmm) {
		return 0, false
	}
	return int(hhmm[0]-'0')*600 + int(hhmm[1]-'0')*60 + int(hhmm[3]-'0')*10 + int(hhmm[4]-'0'), true
}

// Quiet reports whether t falls inside the quiet hours on the clock of
// timeZone (UTC when it does not load): Start is inside, End is not.
func (q QuietHours) Quiet(t time.Time, timeZone string) bool {
	start, okStart := minuteOf(q.Start)
	end, okEnd := minuteOf(q.End)
	if !q.Enabled || !okStart || !okEnd || start == end {
		return false
	}
	loc, err := time.LoadLocation(timeZone)
	if err != nil {
		loc = time.UTC
	}
	local := t.In(loc)
	m := local.Hour()*60 + local.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// validQuietHours checks a quietHours value: an object of exactly enabled,
// start and end, two distinct "HH:MM" times.
func validQuietHours(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 {
		return false
	}
	for _, k := range []string{"enabled", "start", "end"} {
		if v, ok := fields[k]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return false
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var q QuietHours
	if dec.Decode(&q) != nil {
		return false
	}
	start, okStart := minuteOf(q.Start)
	end, okEnd := minuteOf(q.End)
	return okStart && okEnd && start != end
}
