package alarms

import (
	"encoding/json"
	"time"
)

// a reminder "in N minutes" is at most a week ahead
const maxReminderSeconds = 7 * 86400

type setReminderResult struct {
	Reminder stateReminder `json:"reminder"`
}

// setReminder reads set_reminder's args: an id, a label, and a time with days or a date, or inSeconds from now; a
// soundId and a level are optional
func (p *provider) setReminder(m map[string]json.RawMessage) (action, error) {
	var r Reminder
	var clock, date string
	var days []string
	var seconds int
	if !required(m, "id", &r.ID) || !required(m, "label", &r.Label) {
		return nil, errInvalid
	}
	hasTime, okTime := value(m, "time", &clock)
	hasDays, okDays := value(m, "days", &days)
	hasDate, okDate := value(m, "date", &date)
	hasIn, okIn := value(m, "inSeconds", &seconds)
	_, okSound := value(m, "soundId", &r.SoundID)
	hasLevel, okLevel := value(m, "level", &r.Level)
	if !okTime || !okDays || !okDate || !okIn || !okSound || !okLevel || hasTime == hasIn ||
		hasIn && (hasDays || hasDate) || hasDays && hasDate || hasLevel && r.Level < MinLevel ||
		hasIn && (seconds < 1 || seconds > maxReminderSeconds) {
		return nil, errInvalid
	}
	if hasTime {
		var ok bool
		if r.Hour, r.Minute, ok = ParseTime(clock); !ok {
			return nil, errInvalid
		}
	}
	if hasDays {
		var ok bool
		if r.Days, ok = ParseDays(days); !ok || r.Days == 0 {
			return nil, errInvalid
		}
	}
	if hasDate {
		var ok bool
		if r.Date, ok = ParseDate(date); !ok {
			return nil, errInvalid
		}
	}
	if hasIn {
		// any instant passes valid; the book's clock places it
		r.At = time.Unix(1, 0)
	}
	if !r.valid() {
		return nil, errInvalid
	}
	return func() (any, error) {
		if hasIn {
			r.At = wall(p.book.clock.Now()).Add(time.Duration(seconds) * time.Second).UTC()
		}
		stored, err := p.book.SetReminder(r)
		if err != nil {
			return nil, refusal(err)
		}
		return setReminderResult{Reminder: p.reminderState(stored)}, nil
	}, nil
}

// cancelReminder removes the reminder with an id, or all
func (p *provider) cancelReminder(m map[string]json.RawMessage) (action, error) {
	var id string
	var all bool
	hasID, okID := value(m, "id", &id)
	hasAll, okAll := value(m, "all", &all)
	if !okID || !okAll || count(hasID, hasAll) != 1 || hasID && !ValidID(id) || hasAll && !all {
		return nil, errInvalid
	}
	return func() (any, error) {
		var gone []string
		if hasID {
			if p.book.CancelReminder(id) {
				gone = []string{id}
			}
		} else {
			gone = p.book.CancelAllReminders()
		}
		return cancelled(gone)
	}, nil
}
