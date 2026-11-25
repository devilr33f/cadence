package alarmview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

var t0 = time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC)

const base = `{"alarms":[{"id":"b","time":"08:00","level":50,"next":"2026-11-17T08:00:00Z"},` +
	`{"id":"a","time":"07:00","days":["mon","tue"],"level":50,"next":"2026-11-16T07:00:00Z"}],` +
	`"timers":[{"id":"t1","seconds":300,"endsAt":"2026-11-16T07:05:00Z","next":"2026-11-16T07:05:00Z"}],` +
	`"ringing":null,"snoozed":[],"lastRing":null}`

// a state with reminders, and a newer kind, reads them by label and next; without them it reads as before
func TestParseKnowsRemindersAndNewerKinds(t *testing.T) {
	st, ok := Parse(json.RawMessage(base))
	if !ok || len(st.Items) != 3 || st.Items[0].ID != "a" || st.Items[1].ID != "b" || st.Items[2].Kind != KindTimer ||
		!st.Items[2].Next.Equal(t0.Add(5*time.Minute)) || st.Items[2].Seconds != 300 {
		t.Fatalf("base = %+v", st.Items)
	}
	raw := strings.Replace(base, `"ringing":null`, `"reminders":[{"id":"r1","time":"09:00","date":"2026-11-16",`+
		`"label":"выключить духовку","next":"2026-11-16T09:00:00Z","soundId":"x"}],"chores":[{"id":"c1","label":"x"},{"label":"no id"}],`+
		`"ringing":{"kind":"reminder","id":"r1"}`, 1)
	st, ok = Parse(json.RawMessage(raw))
	if !ok || len(st.Items) != 5 {
		t.Fatalf("items = %+v", st.Items)
	}
	r, ok := st.Find(Ring{Kind: KindReminder, ID: "r1"})
	if !ok || r.Label != "выключить духовку" || r.Time != "09:00" || st.Ringing == nil || st.Ringing.Kind != KindReminder {
		t.Fatalf("reminder = %+v ringing %+v", r, st.Ringing)
	}
	if st.Items[3].Kind != KindReminder || st.Items[4].Kind != "chore" {
		t.Fatalf("order = %+v", st.Items)
	}
	if _, ok := Parse(json.RawMessage(`[1]`)); ok {
		t.Fatal("a list read as a state")
	}
}

type bus struct {
	mu  sync.Mutex
	got []events.Event
}

func (b *bus) publish(e events.Event) { b.mu.Lock(); b.got = append(b.got, e); b.mu.Unlock() }

func (b *bus) types() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, e := range b.got {
		out = append(out, e.Type)
	}
	b.got = nil
	return out
}

func (b *bus) last() events.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.got[len(b.got)-1]
}

func ringingState(kind, id string) string {
	return strings.Replace(base, `"ringing":null`, `"ringing":{"kind":"`+kind+`","id":"`+id+`"}`, 1)
}

// a ring that begins and ends is published, how it ended read from the state after it
func TestWatcherPublishesRings(t *testing.T) {
	b := &bus{}
	var changed []string
	now := t0
	w := NewWatcher(Deps{Publish: b.publish, Changed: func(d string) { changed = append(changed, d) }, Now: func() time.Time { return now }})
	w.StateChanged("d1", "audio.output", json.RawMessage(`{}`))
	w.StateChanged("d1", Capability, json.RawMessage(base))
	if got := b.types(); strings.Join(got, ",") != events.AlarmsState {
		t.Fatalf("events = %v", got)
	}
	w.StateChanged("d1", Capability, json.RawMessage(ringingState("alarm", "a")))
	if got := b.types(); strings.Join(got, ",") != events.AlarmsState+","+events.AlarmRinging {
		t.Fatalf("events = %v", got)
	}
	if r, since, ok := w.Ringing("d1"); !ok || r.ID != "a" || !since.Equal(t0) {
		t.Fatalf("ringing = %v %v %v", r, since, ok)
	}
	// a snooze
	now = t0.Add(time.Minute)
	snoozed := strings.Replace(base, `"snoozed":[]`, `"snoozed":[{"kind":"alarm","id":"a","until":"2026-11-16T07:10:00Z"}]`, 1)
	snoozed = strings.Replace(snoozed, `"lastRing":null`, `"lastRing":{"kind":"alarm","id":"a","at":"2026-11-16T07:01:00Z","stoppedBy":"snooze"}`, 1)
	w.StateChanged("d1", Capability, json.RawMessage(snoozed))
	b.types()
	w.StateChanged("d1", Capability, json.RawMessage(ringingState("alarm", "a")))
	w.StateChanged("d1", Capability, json.RawMessage(snoozed))
	end := b.last().Data.(events.AlarmRingEndedData)
	if end.Outcome != OutcomeSnoozed || end.StoppedBy != "snooze" || end.SnoozedUntil == nil || !end.SnoozedUntil.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("snoozed = %+v", end)
	}
	// a timeout is missed, a button a stop
	for by, want := range map[string]string{"timeout": OutcomeMissed, "button": OutcomeStopped} {
		w.StateChanged("d1", Capability, json.RawMessage(ringingState("timer", "t1")))
		w.StateChanged("d1", Capability, json.RawMessage(strings.Replace(base, `"lastRing":null`,
			`"lastRing":{"kind":"timer","id":"t1","at":"2026-11-16T07:06:00Z","stoppedBy":"`+by+`"}`, 1)))
		if end := b.last().Data.(events.AlarmRingEndedData); end.Outcome != want || end.Kind != KindTimer {
			t.Fatalf("%s = %+v", by, end)
		}
	}
	// one ring after another without a gap ends the first
	w.StateChanged("d1", Capability, json.RawMessage(ringingState("alarm", "a")))
	b.types()
	w.StateChanged("d1", Capability, json.RawMessage(ringingState("timer", "t1")))
	if got := b.types(); strings.Join(got, ",") != events.AlarmsState+","+events.AlarmRingEnded+","+events.AlarmRinging {
		t.Fatalf("events = %v", got)
	}
	if len(changed) == 0 || changed[0] != "d1" {
		t.Fatalf("changed = %v", changed)
	}
}

func TestSummaryShowsTheHouseholdAndTheRoomsRings(t *testing.T) {
	w := NewWatcher(Deps{Now: func() time.Time { return t0 }})
	stored := map[string]string{
		"tv":      base,
		"kitchen": ringingState("alarm", "a"),
		"bedroom": ringingState("timer", "t1"),
		"hall":    ringingState("alarm", "b"),
	}
	online := map[string]bool{"tv": true, "kitchen": true, "bedroom": true}
	s := NewSummaries(w, SummaryDeps{
		Devices: func(context.Context) ([]Device, error) {
			return []Device{{ID: "bedroom", Name: "Спальня", Room: "b"}, {ID: "tv", Name: "ТВ", Room: "k"},
				{ID: "kitchen", Name: "Кухня", Room: "k"}, {ID: "hall", Name: "Холл", Room: "k"}, {ID: "lamp", Name: "Лампа"}}, nil
		},
		State: func(_ context.Context, dev, c string) (json.RawMessage, bool) {
			raw, ok := stored[dev]
			return json.RawMessage(raw), ok && c == Capability
		},
		Online: func(d string) bool { return online[d] },
		Desired: func(_ context.Context, dev, _ string) (json.RawMessage, error) {
			if dev == "kitchen" {
				return json.RawMessage(`{"timeZone":"UTC","snoozeMinutes":5}`), nil
			}
			return nil, errors.New("none")
		},
	})
	sum, err := s.Summary(context.Background(), "tv")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, d := range sum.GetDevices() {
		order = append(order, d.GetDeviceId())
	}
	if strings.Join(order, ",") != "tv,kitchen,hall,bedroom" || !sum.GetDevices()[0].GetHere() || !sum.GetDevices()[2].GetSameRoom() ||
		sum.GetDevices()[3].GetSameRoom() || len(sum.GetDevices()[0].GetItems()) != 3 {
		t.Fatalf("devices = %v", order)
	}
	// the kitchen's ring is the room's; the hall's device is offline and the bedroom is in another room
	if len(sum.GetRinging()) != 1 || sum.GetRinging()[0].GetDeviceId() != "kitchen" || sum.GetRinging()[0].GetOwn() ||
		sum.GetRinging()[0].GetSnoozeMinutes() != 5 || sum.GetRinging()[0].GetTime() != "07:00" ||
		sum.GetRinging()[0].GetStartedUnixMs() != t0.UnixMilli() {
		t.Fatalf("ringing = %v", sum.GetRinging())
	}
	// the bedroom's own screen sees its ring as its own
	own, _ := s.Summary(context.Background(), "bedroom")
	if len(own.GetRinging()) != 1 || !own.GetRinging()[0].GetOwn() || own.GetRinging()[0].GetSnoozeMinutes() != DefaultSnoozeMinutes ||
		own.GetRevision() <= sum.GetRevision() {
		t.Fatalf("own = %v", own.GetRinging())
	}
}

// an item past the screen's bounds is left out, so the summary still decodes there
func TestSummaryKeepsTheScreensBounds(t *testing.T) {
	w := NewWatcher(Deps{})
	w.StateChanged("d", Capability, json.RawMessage(`{"alarms":[],"timers":[],"ringing":null,`+
		`"verylongkindnameheres":[{"id":"x"}],"reminders":[{"id":"r","time":"09:00:00","days":["monday","tue"]}]}`))
	s := NewSummaries(w, SummaryDeps{Devices: func(context.Context) ([]Device, error) { return []Device{{ID: "d"}}, nil }})
	sum, _ := s.Summary(context.Background(), "d")
	items := sum.GetDevices()[0].GetItems()
	if len(items) != 1 || items[0].GetTime() != "" || len(items[0].GetDays()) != 1 {
		t.Fatalf("items = %v", items)
	}
}

func TestActSendsTheRingsCommands(t *testing.T) {
	type sent struct{ dev, name, args string }
	var got []sent
	var fail error
	m := ActMethod(ActDeps{Command: func(_ context.Context, dev, c, name string, args []byte) (json.RawMessage, error) {
		if c != Capability {
			t.Fatalf("capability %q", c)
		}
		got = append(got, sent{dev, name, string(args)})
		return nil, fail
	}})
	call := func(args string) calls.Reply {
		return m(context.Background(), calls.Call{DeviceID: "tv", Args: json.RawMessage(args)})
	}
	for args, want := range map[string]sent{
		`{"deviceId":"k","kind":"alarm","id":"a","action":"stop"}`:               {"k", "stop_ring", `{"id":"a","kind":"alarm"}`},
		`{"deviceId":"k","kind":"alarm","id":"a","action":"snooze"}`:             {"k", "snooze", `{"id":"a","kind":"alarm"}`},
		`{"deviceId":"k","kind":"timer","id":"t","action":"snooze","minutes":5}`: {"k", "snooze", `{"id":"t","kind":"timer","minutes":5}`},
		`{"deviceId":"k","kind":"timer","id":"t","action":"cancel"}`:             {"k", "cancel_timer", `{"id":"t"}`},
		`{"deviceId":"k","kind":"reminder","id":"r1","action":"cancel"}`:         {"k", "cancel_reminder", `{"id":"r1"}`},
	} {
		got = nil
		if r := call(args); r.Status != rpc.StatusOK || len(got) != 1 || got[0] != want {
			t.Fatalf("%s: %v %v", args, r, got)
		}
	}
	// never an alarm's cancel or set, nor a press it cannot read
	for _, args := range []string{
		`{"deviceId":"k","kind":"alarm","id":"a","action":"cancel"}`,
		`{"deviceId":"k","kind":"alarm","id":"a","action":"set"}`,
		`{"deviceId":"k","kind":"alarm","id":"A","action":"stop"}`,
		`{"deviceId":"","kind":"alarm","id":"a","action":"stop"}`,
		`{"deviceId":"k","kind":"alarm","id":"a","action":"snooze","minutes":61}`,
		`{"deviceId":"k","kind":"alarm","id":"a","action":"stop","level":100}`,
	} {
		got = nil
		if r := call(args); r.Status != rpc.StatusInvalid || len(got) != 0 {
			t.Fatalf("%s: %v %v", args, r, got)
		}
	}
	for err, code := range map[error]string{
		commands.ErrOffline:                            "offline",
		&commands.DeviceError{Code: "not_found"}:       "not_found",
		&commands.DeviceError{Code: "wake_required"}:   "wake_required",
		&commands.InvalidArgumentsError{Path: "/kind"}: "invalid",
		errors.New("x"):                                "failed",
	} {
		fail = err
		r := call(`{"deviceId":"k","kind":"alarm","id":"a","action":"stop"}`)
		if r.Status != rpc.StatusRefused || r.JSON.(map[string]string)["code"] != code {
			t.Fatalf("%v: %v", err, r)
		}
	}
}
