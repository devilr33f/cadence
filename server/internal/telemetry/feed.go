package telemetry

import (
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/events"
)

// Publisher is the event bus; *events.Bus is one.
type Publisher interface{ Publish(e events.Event) }

// LogLine is a log_line event's data: one stored line, deviceId empty for the
// server's own.
type LogLine struct {
	DeviceID string    `json:"deviceId,omitempty"`
	Source   string    `json:"source"`
	Severity int16     `json:"severity"`
	Time     time.Time `json:"ts"`
	Body     string    `json:"body"`
}

// PublishLogs returns a LogStore listener that publishes each stored row as a
// log_line event while tailing says a feed subscribed to logs (the logs topic
// is opt-in, so mostly none did); the live feed limits how many each peer gets.
func PublishLogs(bus Publisher, tailing func() bool) func(rows []LogRow) {
	return func(rows []LogRow) {
		for _, r := range rows {
			// asked again each row: a feed that subscribes meanwhile gets the rest
			if !tailing() {
				return
			}
			line := LogLine{Source: r.Source, Severity: r.Severity, Time: r.Time, Body: r.Body}
			if r.DeviceID.Valid {
				line.DeviceID = r.DeviceID.String()
			}
			bus.Publish(events.Event{Type: events.LogLine, DeviceID: line.DeviceID, At: r.Time, Data: line})
		}
	}
}
