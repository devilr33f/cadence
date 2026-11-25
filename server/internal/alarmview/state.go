// Package alarmview is the server's view of the devices' alarms capability: it reads each device's reported state
// (alarms, timers, reminders and any newer kind by its label and next ring), publishes alarms_state, alarm_ringing and
// alarm_ring_ended on the bus, builds the household's AlarmsSummary each screen is pushed, and serves the alarms.act
// rpc a screen's press stops, snoozes or cancels with.
//
// A reminder's label is the owner's own setting (owner, 2026-11-16): it goes to screens, the feed and Home Assistant,
// never to a log; lines carry ids, kinds and codes alone (rule 8). Nothing here sets an alarm (rule 7).
package alarmview

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Capability is the device capability this view reads.
const Capability = "alarms"

// the kinds the device names in its state, by the list that holds them
const (
	KindAlarm    = "alarm"
	KindTimer    = "timer"
	KindReminder = "reminder"
)

// Item is an alarm, a timer, a reminder or a kind a newer device reports.
type Item struct {
	Kind, ID string
	// a reminder's label, "" for none
	Label string
	// an alarm's or a reminder's HH:MM, "" for a timer
	Time string
	Days []string
	Date string
	// when it rings next (a snooze included, a timer's end); zero while unknown
	Next time.Time
	// a timer's length
	Seconds int
	Snoozed bool
}

// Ring names a ring: the one that sounds now, or the last one that stopped.
type Ring struct {
	Kind, ID string
}

// LastRing is the ring that stopped last, how and when.
type LastRing struct {
	Ring
	At        time.Time
	StoppedBy string
}

// Snooze is a ring put off until a time.
type Snooze struct {
	Ring
	Until time.Time
}

// State is a device's alarms state as the server reads it.
type State struct {
	// every kind's items, each kind by its next ring
	Items    []Item
	Ringing  *Ring
	Snoozed  []Snooze
	LastRing *LastRing
}

// Find is the item of a kind and id, false for none.
func (s State) Find(r Ring) (Item, bool) {
	for _, it := range s.Items {
		if it.Kind == r.Kind && it.ID == r.ID {
			return it, true
		}
	}
	return Item{}, false
}

// SnoozedUntil is when the ring's snooze ends, false when it is not snoozed.
func (s State) SnoozedUntil(r Ring) (time.Time, bool) {
	for _, z := range s.Snoozed {
		if z.Ring == r {
			return z.Until, true
		}
	}
	return time.Time{}, false
}

// the state's keys that are no list of items
var notItems = map[string]bool{"ringing": true, "snoozed": true, "lastRing": true}

type rawItem struct {
	ID      string   `json:"id"`
	Time    string   `json:"time"`
	Days    []string `json:"days"`
	Date    string   `json:"date"`
	Label   string   `json:"label"`
	Next    string   `json:"next"`
	EndsAt  string   `json:"endsAt"`
	Seconds int      `json:"seconds"`
}

type rawRing struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	At        string `json:"at"`
	StoppedBy string `json:"stoppedBy"`
	Until     string `json:"until"`
}

// Parse reads a device's alarms state; false for one that is no object. A list it does not know (reminders before
// this server knew them, a newer kind) is read by its items' id, label and next, its kind the list's name without its
// s; an item without an id is skipped.
func Parse(raw json.RawMessage) (State, bool) {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || m == nil {
		return State{}, false
	}
	var st State
	for key, v := range m {
		if notItems[key] {
			continue
		}
		var list []rawItem
		if json.Unmarshal(v, &list) != nil {
			continue
		}
		kind := strings.TrimSuffix(key, "s")
		for _, it := range list {
			if it.ID == "" {
				continue
			}
			next := instant(it.Next)
			if next.IsZero() {
				next = instant(it.EndsAt)
			}
			st.Items = append(st.Items, Item{
				Kind: kind, ID: it.ID, Label: it.Label, Time: it.Time, Days: it.Days, Date: it.Date, Next: next,
				Seconds: it.Seconds,
			})
		}
	}
	var ringing *rawRing
	if json.Unmarshal(m["ringing"], &ringing) == nil && ringing != nil && ringing.Kind != "" && ringing.ID != "" {
		st.Ringing = &Ring{Kind: ringing.Kind, ID: ringing.ID}
	}
	var snoozed []rawRing
	if json.Unmarshal(m["snoozed"], &snoozed) == nil {
		for _, z := range snoozed {
			if z.Kind != "" && z.ID != "" {
				st.Snoozed = append(st.Snoozed, Snooze{Ring: Ring{Kind: z.Kind, ID: z.ID}, Until: instant(z.Until)})
			}
		}
	}
	var last *rawRing
	if json.Unmarshal(m["lastRing"], &last) == nil && last != nil && last.Kind != "" && last.ID != "" {
		st.LastRing = &LastRing{Ring: Ring{Kind: last.Kind, ID: last.ID}, At: instant(last.At), StoppedBy: last.StoppedBy}
	}
	for i := range st.Items {
		_, st.Items[i].Snoozed = st.SnoozedUntil(Ring{Kind: st.Items[i].Kind, ID: st.Items[i].ID})
	}
	sort.SliceStable(st.Items, func(i, j int) bool { return before(st.Items[i], st.Items[j]) })
	return st, true
}

// before orders by kind (alarms, timers, reminders, then the rest by name), then by next ring, unknown last, then id
func before(a, b Item) bool {
	if ka, kb := kindOrder(a.Kind), kindOrder(b.Kind); ka != kb {
		return ka < kb
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	switch {
	case a.Next.IsZero() != b.Next.IsZero():
		return !a.Next.IsZero()
	case !a.Next.Equal(b.Next):
		return a.Next.Before(b.Next)
	}
	return a.ID < b.ID
}

func kindOrder(k string) int {
	switch k {
	case KindAlarm:
		return 0
	case KindTimer:
		return 1
	case KindReminder:
		return 2
	}
	return 3
}

func instant(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
