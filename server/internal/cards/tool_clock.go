package cards

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// a timer card outlives its countdown by this, so the screen shows it end
const timerGrace = time.Minute

type timerOut struct {
	Seconds    int `json:"seconds"`
	RemainingS int `json:"remainingS"`
}

// timerCards: a timer set ticks on the screen; a list shows each one's
// countdown; a cancel says what went
func timerCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Timer     *timerOut  `json:"timer"`
		Timers    []timerOut `json:"timers"`
		Cancelled []timerOut `json:"cancelled"`
	}
	if !decode(r, &out) {
		return nil
	}
	countdown := func(t timerOut, label string) *cardsv1.Countdown {
		c := &cardsv1.Countdown{EndsAtMs: r.At.Add(time.Duration(t.RemainingS) * time.Second).UnixMilli(),
			Label: clip(label, 40), Format: cardsv1.CountdownFormat_COUNTDOWN_FORMAT_MM_SS}
		if t.RemainingS >= 3600 {
			c.Format = cardsv1.CountdownFormat_COUNTDOWN_FORMAT_HH_MM_SS
		}
		return c
	}
	switch {
	case out.Timer != nil:
		t := *out.Timer
		subj := &toolCard{id: OpaqueID("timer", r.Key), build: func(rr *Render) (*cardsv1.CardBlock, string) {
			length := durationLabel(rr, t.Seconds)
			b := &cardsv1.CardBlock{Title: rr.T("timer.title"), Subtitle: clip(length, 80), Icon: "timer",
				Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(countdown(t, rr.T("timer.left")))}
			return b, rr.T("timer.fallback", "length", length)
		}}
		return []Subject{&ttlCard{Subject: subj, ttl: time.Duration(t.RemainingS)*time.Second + timerGrace}}
	case out.Timers != nil:
		timers := out.Timers[:min(len(out.Timers), 4)]
		longest := 0
		for _, t := range timers {
			longest = max(longest, t.RemainingS)
		}
		subj := &toolCard{id: OpaqueID("timers", r.Key), build: func(rr *Render) (*cardsv1.CardBlock, string) {
			b := &cardsv1.CardBlock{Title: rr.T("timer.list"), Icon: "timer", Tone: cardsv1.Tone_TONE_NEUTRAL}
			if len(timers) == 0 {
				return done(rr, rr.T("timer.list"), "timer", rr.T("timer.none"), cardsv1.StateKind_STATE_KIND_OFF, ""), rr.T("timer.none")
			}
			for _, t := range timers {
				b.Children = append(b.Children, pcards.N(countdown(t, durationLabel(rr, t.Seconds))))
			}
			return b, rr.T("timer.count", "n", strconv.Itoa(len(out.Timers)))
		}}
		return []Subject{&ttlCard{Subject: subj, ttl: time.Duration(longest)*time.Second + timerGrace}}
	case out.Cancelled != nil:
		gone := out.Cancelled
		return card("timer-gone", r, func(rr *Render) (*cardsv1.CardBlock, string) {
			var lengths []string
			for _, t := range gone {
				if t.Seconds > 0 {
					lengths = append(lengths, durationLabel(rr, t.Seconds))
				}
			}
			state := rr.T("timer.cancelled")
			return done(rr, rr.T("timer.title"), "timer", state, cardsv1.StateKind_STATE_KIND_OFF, strings.Join(lengths, ", ")), state
		})
	}
	return nil
}

type alarmOut struct {
	Time string   `json:"time"`
	Days []string `json:"days"`
	Date string   `json:"date"`
	// its music's title, the user's setting
	Sound string `json:"sound"`
}

// alarmCards: the alarm set with its days, the list, or what a cancel took
func alarmCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Alarm     *alarmOut  `json:"alarm"`
		Alarms    []alarmOut `json:"alarms"`
		Cancelled []alarmOut `json:"cancelled"`
	}
	if !decode(r, &out) {
		return nil
	}
	switch {
	case out.Alarm != nil:
		a := *out.Alarm
		return card("alarm", r, func(rr *Render) (*cardsv1.CardBlock, string) {
			when := alarmDays(rr, a)
			b := &cardsv1.CardBlock{Title: rr.T("alarm.title"), Subtitle: clip(a.Sound, 80), Icon: "alarm", Tone: cardsv1.Tone_TONE_NEUTRAL,
				Children: pcards.Nodes(&cardsv1.Row{Gap: cardsv1.Gap_GAP_M, Children: pcards.Nodes(
					&cardsv1.Value{Value: clip(a.Time, 24), Label: clip(when, 40)},
					&cardsv1.State{Label: rr.T("alarm.on"), Kind: cardsv1.StateKind_STATE_KIND_ON},
				)})}
			return b, rr.T("alarm.fallback", "time", a.Time, "when", when)
		})
	case out.Alarms != nil:
		list := out.Alarms
		return card("alarms", r, func(rr *Render) (*cardsv1.CardBlock, string) {
			items := make([][2]string, 0, len(list))
			for _, a := range list {
				items = append(items, [2]string{a.Time, alarmDays(rr, a)})
			}
			kv := rows(items)
			if kv == nil {
				return done(rr, rr.T("alarm.list"), "alarm", rr.T("alarm.none"), cardsv1.StateKind_STATE_KIND_OFF, ""), rr.T("alarm.none")
			}
			b := &cardsv1.CardBlock{Title: rr.T("alarm.list"), Icon: "alarm", Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(kv)}
			return b, rr.T("alarm.count", "n", strconv.Itoa(len(list)))
		})
	case out.Cancelled != nil:
		gone := out.Cancelled
		return card("alarm-gone", r, func(rr *Render) (*cardsv1.CardBlock, string) {
			var times []string
			for _, a := range gone {
				if a.Time != "" {
					times = append(times, a.Time)
				}
			}
			state := rr.T("alarm.cancelled")
			return done(rr, rr.T("alarm.title"), "alarm", state, cardsv1.StateKind_STATE_KIND_OFF, strings.Join(times, ", ")), state
		})
	}
	return nil
}

type reminderOut struct {
	Text      string   `json:"text"`
	Time      string   `json:"time"`
	Days      []string `json:"days"`
	Date      string   `json:"date"`
	InMinutes int      `json:"inMinutes"`
}

// reminderWhen words when a reminder rings: in some minutes, or its time and repeat
func reminderWhen(rr *Render, m reminderOut) string {
	if m.Time == "" {
		if m.InMinutes > 0 {
			return rr.T("reminder.in", "length", durationLabel(rr, m.InMinutes*60))
		}
		return ""
	}
	return m.Time + ", " + alarmDays(rr, alarmOut{Days: m.Days, Date: m.Date})
}

// reminderCards: the reminder set with its words and when, the list, or what a cancel took. the words are the user's
// own setting, shown on their screen, never logged
func reminderCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Reminder  *reminderOut  `json:"reminder"`
		Reminders []reminderOut `json:"reminders"`
		Cancelled []reminderOut `json:"cancelled"`
	}
	if !decode(r, &out) {
		return nil
	}
	switch {
	case out.Reminder != nil:
		m := *out.Reminder
		return card("reminder", r, func(rr *Render) (*cardsv1.CardBlock, string) {
			when := reminderWhen(rr, m)
			b := &cardsv1.CardBlock{Title: rr.T("reminder.title"), Subtitle: clip(when, 80), Icon: "bell",
				Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(&cardsv1.Text{Text: clip(m.Text, 280), Style: cardsv1.TextStyle_TEXT_STYLE_BODY})}
			return b, rr.T("reminder.fallback", "when", when)
		})
	case out.Reminders != nil:
		list := out.Reminders
		return card("reminders", r, func(rr *Render) (*cardsv1.CardBlock, string) {
			items := make([][2]string, 0, len(list))
			for _, m := range list {
				items = append(items, [2]string{reminderWhen(rr, m), m.Text})
			}
			kv := rows(items)
			if kv == nil {
				return done(rr, rr.T("reminder.list"), "bell", rr.T("reminder.none"), cardsv1.StateKind_STATE_KIND_OFF, ""), rr.T("reminder.none")
			}
			b := &cardsv1.CardBlock{Title: rr.T("reminder.list"), Icon: "bell", Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(kv)}
			return b, rr.T("reminder.count", "n", strconv.Itoa(len(list)))
		})
	case out.Cancelled != nil:
		gone := out.Cancelled
		return card("reminder-gone", r, func(rr *Render) (*cardsv1.CardBlock, string) {
			var texts []string
			for _, m := range gone {
				texts = append(texts, m.Text)
			}
			state := rr.T("reminder.cancelled")
			return done(rr, rr.T("reminder.title"), "bell", state, cardsv1.StateKind_STATE_KIND_OFF, clip(strings.Join(texts, "; "), 120)), state
		})
	}
	return nil
}

var weekOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// alarmDays words an alarm's repeat: every day, weekdays, weekends, the days,
// its one date, or once
func alarmDays(rr *Render, a alarmOut) string {
	days := slices.Clone(a.Days)
	slices.SortFunc(days, func(x, y string) int { return slices.Index(weekOrder, x) - slices.Index(weekOrder, y) })
	days = slices.Compact(days)
	switch {
	case len(days) == 7:
		return rr.T("alarm.daily")
	case slices.Equal(days, weekOrder[:5]):
		return rr.T("alarm.weekdays")
	case slices.Equal(days, weekOrder[5:]):
		return rr.T("alarm.weekends")
	case len(days) > 0:
		names := make([]string, 0, len(days))
		for _, d := range days {
			names = append(names, rr.T("day."+d))
		}
		return strings.Join(names, ", ")
	case a.Date != "":
		if t, err := time.Parse(time.DateOnly, a.Date); err == nil {
			if strings.HasPrefix(strings.ToLower(rr.Lang), "ru") {
				return t.Format("02.01")
			}
			return t.Format("Jan 2")
		}
		return a.Date
	}
	return rr.T("alarm.once")
}

// dndCards: do-not-disturb on until a time, or off
func dndCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		On    bool   `json:"on"`
		Until string `json:"until"`
	}
	if !decode(r, &out) {
		return nil
	}
	return card("dnd", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		if !out.On {
			state := rr.T("dnd.off")
			return done(rr, rr.T("dnd.title"), "moon", state, cardsv1.StateKind_STATE_KIND_OFF, ""), rr.T("dnd.title") + ": " + state
		}
		b := &cardsv1.CardBlock{Title: rr.T("dnd.title"), Icon: "moon", Tone: cardsv1.Tone_TONE_NEUTRAL,
			Children: pcards.Nodes(&cardsv1.Row{Gap: cardsv1.Gap_GAP_M, Children: pcards.Nodes(
				&cardsv1.State{Label: rr.T("dnd.on"), Kind: cardsv1.StateKind_STATE_KIND_ON},
				&cardsv1.Value{Value: clip(out.Until, 24), Label: rr.T("dnd.until")},
			)})}
		return b, rr.T("dnd.fallback", "until", out.Until)
	})
}

// durationLabel is a length as the catalog words it: 45 с, 10 мин, 1 ч 30 мин
func durationLabel(rr *Render, seconds int) string {
	switch {
	case seconds < 60:
		return rr.T("duration.s", "n", strconv.Itoa(seconds))
	case seconds < 3600:
		m, s := seconds/60, seconds%60
		if s == 0 {
			return rr.T("duration.m", "n", strconv.Itoa(m))
		}
		return rr.T("duration.m", "n", strconv.Itoa(m)) + " " + rr.T("duration.s", "n", strconv.Itoa(s))
	}
	return rr.T("duration.h", "h", strconv.Itoa(seconds/3600), "m", strconv.Itoa(seconds%3600/60))
}

// ttlCard is a card that expires with what it shows (a timer's end)
type ttlCard struct {
	Subject
	ttl time.Duration
}

func (c *ttlCard) Render(ctx context.Context, r *Render) (*cardsv1.Card, error) {
	card, err := c.Subject.Render(ctx, r)
	if err == nil {
		card.TtlS = uint32(min(max(c.ttl, timerGrace)/time.Second, pcards.MaxTTLSeconds))
		card.Dismissable = true
	}
	return card, err
}
