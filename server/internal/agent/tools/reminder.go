package tools

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/ringsounds"
)

const (
	// the device keeps a label of this many runes for its screens; the server keeps the whole text
	reminderLabelRunes = 80
	maxReminderText    = ringsounds.MaxTextRunes
	// a week ahead, the device's own bound
	maxReminderMinutes = 7 * 24 * 60
)

const reminderInput = `{
	"type": "object",
	"properties": {
		"action": {"type": "string", "enum": ["set", "cancel", "list", "snooze"]},
		"text": {"type": "string", "minLength": 1, "maxLength": 300},
		"time": {"type": "string", "pattern": "^(?:[01]?\\d|2[0-3]):[0-5]\\d$"},
		"days": {"type": "array", "maxItems": 7, "items": {"type": "string",
			"enum": ["mon", "tue", "wed", "thu", "fri", "sat", "sun", "daily", "weekdays", "weekends"]}},
		"date": {"type": "string", "pattern": "^\\d{4}-\\d{2}-\\d{2}$"},
		"inMinutes": {"type": "integer", "minimum": 1, "maximum": 10080},
		"level": {"type": "integer", "minimum": 1, "maximum": 100},
		"minutes": {"type": "integer", "minimum": 1, "maximum": 60},
		"all": {"type": "boolean"},
		` + deviceProp + `
	},
	"required": ["action"],
	"additionalProperties": false
}`

var reminderFields = map[string][]string{
	"set":    {"text", "time", "days", "date", "inMinutes", "level", "device"},
	"cancel": {"text", "time", "all", "device"},
	"list":   {"device"}, "snooze": {"minutes", "device"},
}

// RingSounds makes the sounds rings stream from the server (ringsounds.Service): a reminder's speech, and what the
// device's sounds are.
type RingSounds interface {
	Speech(ctx context.Context, deviceID, text string) (ringsounds.Sound, error)
	Info(ctx context.Context, deviceID string, ids []string) map[string]ringsounds.Info
}

// Reminder sets, cancels, lists and snoozes the device's reminders (P4): at a time of day, once or on repeat, or in
// some minutes, it rings a chime and speaks the text, which the server synthesizes now and keeps until the reminder
// goes (owner, 2026-11-16: the text is the user's setting, not a transcript). Without a level it rings at the level
// in force and may be set in a follow-up; with one it raises the volume, so it needs a woken session (rule 7). A
// failed synthesis sets it anyway: the device then speaks its bundled «Напоминание».
func Reminder(d AlarmDeps) agent.Tool {
	return agent.Tool{
		Name: "reminder",
		Description: "Set, cancel or list the speaker's reminders: at a time it rings a soft chime and speaks the " +
			"text. set takes text (what the speaker will say, phrased to be spoken, in the user's language, e.g. " +
			"«Напоминаю: выключить духовку»), and either inMinutes (from now) or time (H:MM, 24-hour clock) with " +
			"days for a repeating reminder (mon..sun; daily, weekdays, weekends) or date (YYYY-MM-DD) for another " +
			"day than the time's next one; level (1-100) only when the user names a volume. cancel takes text (words " +
			"of the reminder), time, all, or neither when only one is set. list reads them. " + snoozeHint("reminder") +
			deviceHint + "Call it for every reminder request; nothing is remembered without it. For a countdown " +
			"without words use the timer tool. If the result says spoken false, tell the user the speaker will only " +
			"chime and say «Напоминание».",
		Input:   reminderInput,
		Effects: agent.Write, Output: agent.Trusted,
		// a synthesis takes seconds before the device's own command
		Timeout: alarmsTimeout + 20*time.Second,
		Run: d.logged("reminder_tool", func(ctx context.Context, c agent.Call) (string, json.RawMessage, *agent.ToolError) {
			ctx, c, e := d.target(ctx, c)
			if e != nil {
				return "", nil, e
			}
			st, ok := d.state(ctx, c.DeviceID)
			if !ok {
				return "", nil, noAlarms()
			}
			a, e := parseReminder(c.Args)
			if e != nil {
				return a.action, nil, e
			}
			out, e := d.reminder(ctx, c, st, a)
			return a.action, out, e
		}),
	}
}

type reminderArgs struct {
	action    string
	text      string
	time      string
	days      []string
	date      string
	inMinutes int
	level     *int
	minutes   int
	all       bool
}

func parseReminder(raw json.RawMessage) (reminderArgs, *agent.ToolError) {
	m, ok := fields(raw)
	if !ok {
		return reminderArgs{}, badArgs("The arguments are not a JSON object.")
	}
	var a reminderArgs
	var e *agent.ToolError
	if a.action, e = action(m, reminderFields); e != nil {
		return a, e
	}
	if raw, ok := m["text"]; ok {
		if json.Unmarshal(raw, &a.text) != nil {
			return a, badArgs("text is a string.")
		}
		a.text = strings.TrimSpace(a.text)
		if a.text == "" || utf8.RuneCountInString(a.text) > maxReminderText {
			return a, badArgs("text is 1-300 characters.")
		}
	}
	if raw, ok := m["time"]; ok {
		if a.time, ok = parseClock(raw); !ok {
			return a, badArgs("time is H:MM on a 24-hour clock.")
		}
	}
	if raw, ok := m["days"]; ok {
		if a.days, ok = parseDays(raw); !ok {
			return a, badArgs("days is a list of mon..sun, or daily, weekdays or weekends.")
		}
	}
	if raw, ok := m["date"]; ok {
		if json.Unmarshal(raw, &a.date) != nil || !isoDate.MatchString(a.date) {
			return a, badArgs("date is YYYY-MM-DD.")
		}
		if _, err := time.Parse(time.DateOnly, a.date); err != nil {
			return a, badArgs("date is not a day of the calendar.")
		}
	}
	if raw, ok := m["inMinutes"]; ok {
		if json.Unmarshal(raw, &a.inMinutes) != nil || a.inMinutes < 1 || a.inMinutes > maxReminderMinutes {
			return a, badArgs("inMinutes is 1-10080.")
		}
	}
	if raw, ok := m["level"]; ok {
		var level int
		if json.Unmarshal(raw, &level) != nil || level < 1 || level > 100 {
			return a, badArgs("level is 1-100.")
		}
		a.level = &level
	}
	if raw, ok := m["minutes"]; ok {
		if a.minutes, ok = parseMinutes(raw); !ok {
			return a, badArgs("minutes is 1-60.")
		}
	}
	if raw, ok := m["all"]; ok && json.Unmarshal(raw, &a.all) != nil {
		return a, badArgs("all is true or false.")
	}
	switch {
	case a.action == "set" && a.text == "":
		return a, badArgs("set needs text.")
	case a.action == "set" && (a.time == "") == (a.inMinutes == 0):
		return a, badArgs("set needs time or inMinutes, not both.")
	case a.inMinutes > 0 && (len(a.days) > 0 || a.date != ""):
		return a, badArgs("inMinutes goes alone, without days or date.")
	case a.date != "" && len(a.days) > 0:
		return a, badArgs("Give days for a repeating reminder or date for a one-time one, not both.")
	case a.all && (a.time != "" || a.text != ""):
		return a, badArgs("Give text, time or all, not several.")
	}
	return a, nil
}

type reminderEntry struct {
	ID      string   `json:"id"`
	Time    string   `json:"time"`
	Days    []string `json:"days"`
	Date    string   `json:"date"`
	At      string   `json:"at"`
	Label   string   `json:"label"`
	SoundID string   `json:"soundId"`
	Level   int      `json:"level"`
	Next    string   `json:"next"`
}

type remindersState struct {
	Reminders []reminderEntry `json:"reminders"`
}

// reminderView is a reminder as the model sees it: its words, never its id
type reminderView struct {
	Text      string   `json:"text"`
	Time      string   `json:"time,omitempty"`
	Days      []string `json:"days,omitempty"`
	Date      string   `json:"date,omitempty"`
	InMinutes int      `json:"inMinutes,omitempty"`
	Level     int      `json:"level,omitempty"`
}

type setReminderArgs struct {
	SessionID string   `json:"sessionId,omitempty"`
	ID        string   `json:"id"`
	Time      string   `json:"time,omitempty"`
	Days      []string `json:"days,omitempty"`
	Date      string   `json:"date,omitempty"`
	InSeconds int      `json:"inSeconds,omitempty"`
	Label     string   `json:"label"`
	SoundID   string   `json:"soundId,omitempty"`
	Level     *int     `json:"level,omitempty"`
}

type cancelReminderArgs struct {
	SessionID string `json:"sessionId,omitempty"`
	ID        string `json:"id,omitempty"`
	All       bool   `json:"all,omitempty"`
}

// reminders reads the reminders the device last reported; an older device reports none
func (d AlarmDeps) reminders(ctx context.Context, deviceID string) []reminderEntry {
	raw, ok := d.State(ctx, deviceID, alarmsCapability)
	var st remindersState
	if !ok || json.Unmarshal(raw, &st) != nil {
		return nil
	}
	return st.Reminders
}

// texts are the reminders' whole words: the server's speech of each, else the device's label
func (d AlarmDeps) texts(ctx context.Context, deviceID string, list []reminderEntry) map[string]string {
	out := make(map[string]string, len(list))
	var ids []string
	for _, r := range list {
		out[r.ID] = r.Label
		if r.SoundID != "" {
			ids = append(ids, r.SoundID)
		}
	}
	if d.Sounds == nil || len(ids) == 0 {
		return out
	}
	info := d.Sounds.Info(ctx, deviceID, ids)
	for _, r := range list {
		if i, ok := info[r.SoundID]; ok && i.Kind == ringsounds.KindSpeech && i.Title != "" {
			out[r.ID] = i.Title
		}
	}
	return out
}

func (d AlarmDeps) reminderView(r reminderEntry, text string) reminderView {
	v := reminderView{Text: text, Time: r.Time, Days: r.Days, Date: r.Date, Level: r.Level}
	if r.At != "" {
		if left, ok := remaining(r.At, d.now()); ok {
			v.InMinutes = int(math.Ceil(float64(left) / 60))
		}
	}
	return v
}

func (d AlarmDeps) reminder(ctx context.Context, c agent.Call, _ alarmsState, a reminderArgs) (json.RawMessage, *agent.ToolError) {
	list := d.reminders(ctx, c.DeviceID)
	switch a.action {
	case "list":
		texts := d.texts(ctx, c.DeviceID, list)
		views := make([]reminderView, 0, len(list))
		for _, r := range list {
			views = append(views, d.reminderView(r, texts[r.ID]))
		}
		return encode(map[string][]reminderView{"reminders": views})
	case "snooze":
		return d.snooze(ctx, c, "reminder", a.minutes)
	case "set":
		// rule 7: a reminder with a level raises the volume when it rings
		if a.level != nil && !agent.Woken(c.Trigger) {
			return nil, agent.WakeRequired()
		}
		return d.setReminder(ctx, c, list, a)
	}
	return d.cancelReminder(ctx, c, list, a)
}

func (d AlarmDeps) setReminder(ctx context.Context, c agent.Call, list []reminderEntry, a reminderArgs) (json.RawMessage, *agent.ToolError) {
	args := setReminderArgs{
		SessionID: c.SessionID, Time: a.time, Days: a.days, Date: a.date, InSeconds: a.inMinutes * 60,
		Label: ReminderLabel(a.text), Level: a.level,
		ID: newID(func(id string) bool {
			return slices.ContainsFunc(list, func(r reminderEntry) bool { return r.ID == id })
		}),
	}
	spoken := false
	if d.Sounds != nil {
		// the speech first: a reminder whose speech failed still rings, with the bundled prompt
		if s, err := d.Sounds.Speech(ctx, c.DeviceID, a.text); err == nil {
			args.SoundID, spoken = s.ID, true
		} else if ctx.Err() != nil {
			return nil, &agent.ToolError{Code: "timeout", Message: "Setting the reminder took too long."}
		}
	}
	res, err := d.command(ctx, c, "set_reminder", args)
	if err != nil {
		return nil, alarmsError(err, "reminder_failed", "reminder")
	}
	var out struct {
		Reminder *reminderEntry `json:"reminder"`
	}
	if json.Unmarshal(res, &out) != nil || out.Reminder == nil {
		return nil, failedAnswer("reminder_failed")
	}
	return encode(struct {
		Reminder reminderView `json:"reminder"`
		Spoken   bool         `json:"spoken"`
	}{d.reminderView(*out.Reminder, a.text), spoken})
}

// cancelReminder removes the reminders whose words hold text, those at a time, all, or the only one
func (d AlarmDeps) cancelReminder(ctx context.Context, c agent.Call, list []reminderEntry, a reminderArgs) (json.RawMessage, *agent.ToolError) {
	texts := d.texts(ctx, c.DeviceID, list)
	var picked []reminderEntry
	switch {
	case a.all:
	case a.text != "":
		want := foldText(a.text)
		for _, r := range list {
			if strings.Contains(foldText(texts[r.ID]), want) {
				picked = append(picked, r)
			}
		}
	case a.time != "":
		for _, r := range list {
			if r.Time == a.time {
				picked = append(picked, r)
			}
		}
	default:
		picked = list
	}
	if !a.all {
		switch {
		case len(picked) == 0:
			return nil, &agent.ToolError{Code: "not_found", Message: "No reminder like that is set."}
		case len(picked) > 1 && a.time == "":
			var names []string
			for _, r := range picked {
				names = append(names, texts[r.ID])
			}
			return nil, &agent.ToolError{
				Code: "ambiguous", Message: "Several reminders match: " + strings.Join(names, "; ") + ".",
				Hint: "Ask the user which one, then call again with words of it.", Candidates: names,
			}
		}
	}
	var gone []reminderView
	ids := make([]string, 0, len(picked))
	if a.all {
		res, err := d.command(ctx, c, "cancel_reminder", cancelReminderArgs{SessionID: c.SessionID, All: true})
		if err != nil {
			return nil, alarmsError(err, "reminder_failed", "reminder")
		}
		if ids, _ = cancelled(res); len(ids) == 0 {
			return nil, failedAnswer("reminder_failed")
		}
	} else {
		for _, r := range picked {
			_, err := d.command(ctx, c, "cancel_reminder", cancelReminderArgs{SessionID: c.SessionID, ID: r.ID})
			if err != nil {
				// one already gone is not a failure of the others
				if e := alarmsError(err, "reminder_failed", "reminder"); e.Code != "not_found" {
					return nil, e
				}
				continue
			}
			ids = append(ids, r.ID)
		}
	}
	for _, id := range ids {
		if i := slices.IndexFunc(list, func(r reminderEntry) bool { return r.ID == id }); i >= 0 {
			v := d.reminderView(list[i], texts[id])
			v.Level, v.InMinutes = 0, 0
			gone = append(gone, v)
		}
	}
	if len(gone) == 0 {
		return nil, &agent.ToolError{Code: "not_found", Message: "No reminder like that is set."}
	}
	return encode(map[string][]reminderView{"cancelled": gone})
}

// ReminderLabel is the device's short form of a reminder's text: its first 80 runes, cut at a word with an ellipsis, no control
// characters
func ReminderLabel(text string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	clean = strings.Join(strings.Fields(clean), " ")
	runes := []rune(clean)
	if len(runes) <= reminderLabelRunes {
		return clean
	}
	cut := string(runes[:reminderLabelRunes-1])
	if i := strings.LastIndex(cut, " "); i > reminderLabelRunes/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

func foldText(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
