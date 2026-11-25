package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"time"
)

// clockRe is an alarm's HH:MM as the device's state has it
var clockRe = regexp.MustCompile(`^(?:[01]\d|2[0-3]):[0-5]\d$`)

// AfterRing is the ring a wake stopped right before its session began: its kind ("alarm" or "timer"), the device's
// id of it, and how long before the session it stopped.
type AfterRing struct {
	Kind    string
	ID      string
	Stopped time.Duration
}

// afterRingNote tells the model that the user just woke the speaker out of a ring, so that a request for more time
// is a snooze of it (P2); the alarm's time or the timer's length comes from the device's alarms state, never its id.
// Empty without one, or for a kind it does not know.
func (r *Runner) afterRingNote(ctx context.Context, deviceID string, ar *AfterRing) string {
	if ar == nil || !slices.Contains([]string{"alarm", "timer"}, ar.Kind) {
		return ""
	}
	what := "An " + ar.Kind
	if ar.Kind == "timer" {
		what = "A timer"
	}
	if raw, ok := r.d.State(ctx, deviceID, "alarms"); ok {
		what += ringDetail(raw, ar)
	}
	ago := max(0, int(ar.Stopped.Round(time.Second).Seconds()))
	return fmt.Sprintf("%s was ringing and the user stopped it with the wake word %d s ago. If they ask for more "+
		"time («ещё 5 минут», «snooze»), call the %s tool with action snooze and minutes; never cancel or set it again.",
		what, ago, ar.Kind)
}

// ringDetail is " (07:00)" for an alarm the state still names; a timer that rang is gone from it
func ringDetail(raw json.RawMessage, ar *AfterRing) string {
	var st struct {
		Alarms []struct {
			ID   string `json:"id"`
			Time string `json:"time"`
		} `json:"alarms"`
	}
	if ar.Kind != "alarm" || json.Unmarshal(raw, &st) != nil {
		return ""
	}
	for _, a := range st.Alarms {
		if a.ID == ar.ID && clockRe.MatchString(a.Time) {
			return " (" + a.Time + ")"
		}
	}
	return ""
}
