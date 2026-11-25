package alarms_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

func reminderAt(id, label string, hour, minute int) alarms.Reminder {
	return alarms.Reminder{ID: id, Label: label, Hour: hour, Minute: minute}
}

func TestAReminderAtAnInstantRingsWithoutAZoneAndOnce(t *testing.T) {
	f := newFixture(t)
	b := f.openUnzoned(at(26, 6, 0, 0))
	r, err := b.SetReminder(alarms.Reminder{ID: "oven", Label: "выключить духовку", At: at(26, 6, 20, 0), SoundID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.At.Equal(at(26, 6, 20, 0)) {
		t.Fatalf("stored %v", r.At)
	}
	if rings := b.TakeDue(at(26, 6, 19, 59)); len(rings) != 0 {
		t.Fatalf("early %v", rings)
	}
	rings := b.TakeDue(at(26, 6, 20, 0))
	if len(rings) != 1 || rings[0].Kind != alarms.KindReminder || rings[0].ID != "oven" || rings[0].SoundID != "s1" ||
		rings[0].Level != 0 {
		t.Fatalf("rings %+v", rings)
	}
	if len(b.Reminders()) != 0 || len(b.TakeDue(at(26, 6, 21, 0))) != 0 {
		t.Fatal("a one-time reminder rang twice")
	}
	if f.logs.contains("духовк") {
		t.Fatal("the label was logged")
	}
}

func TestACalendarReminderWaitsForAZoneRepeatsAndSurvivesARestart(t *testing.T) {
	f := newFixture(t)
	b := f.openUnzoned(at(26, 6, 0, 0))
	daily := reminderAt("pills", "таблетки", 9, 0)
	daily.Days = alarms.EveryDay
	daily.Level = 40
	if _, err := b.SetReminder(daily); !errors.Is(err, alarms.ErrNoZone) {
		t.Fatalf("unzoned: %v", err)
	}
	if err := b.SetZone("Asia/Bishkek"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetReminder(daily); err != nil {
		t.Fatal(err)
	}
	once := reminderAt("call", "позвонить маме", 8, 0)
	if _, err := b.SetReminder(once); err != nil {
		t.Fatal(err)
	}
	// a restart reads both back, the one-time one with its pinned day
	b = f.open(at(26, 7, 0, 0))
	got := b.Reminders()
	if len(got) != 2 || got[0].Label != "таблетки" || got[0].Level != 40 || got[1].Date != date(2026, 10, 26) {
		t.Fatalf("reloaded %+v", got)
	}
	if next, ok := b.NextOfReminder(got[0]); !ok || !next.Equal(at(26, 9, 0, 0)) {
		t.Fatalf("next %v %t", next, ok)
	}
	wantIDs(t, "8:00", ringIDs(b.TakeDue(at(26, 8, 0, 0))), "call")
	wantIDs(t, "9:00", ringIDs(b.TakeDue(at(26, 9, 0, 0))), "pills")
	wantIDs(t, "the next day", ringIDs(b.TakeDue(at(27, 9, 0, 0))), "pills")
	if len(b.Reminders()) != 1 {
		t.Fatalf("left %+v", b.Reminders())
	}
	if !b.CancelReminder("pills") || b.CancelReminder("pills") {
		t.Fatal("cancel")
	}
}

func TestAReminderTheBookCannotHoldIsRefused(t *testing.T) {
	f := newFixture(t)
	b := f.open(at(26, 6, 0, 0))
	for _, r := range []alarms.Reminder{
		reminderAt("x", "", 7, 0),
		reminderAt("x", strings.Repeat("я", alarms.MaxLabelRunes+1), 7, 0),
		reminderAt("x", "line\nbreak", 7, 0),
		reminderAt("x", "\xff", 7, 0),
		{ID: "x", Label: "a", Hour: 7, At: at(26, 7, 0, 0)},
		{ID: "x", Label: "a", Hour: 7, Level: 101},
		{ID: "x", Label: "a", Hour: 7, SoundID: "Bad"},
	} {
		if _, err := b.SetReminder(r); !errors.Is(err, alarms.ErrInvalid) {
			t.Errorf("%+v: %v", r, err)
		}
	}
	if _, err := b.SetReminder(alarms.Reminder{ID: "x", Label: "a", At: at(26, 5, 0, 0)}); !errors.Is(err, alarms.ErrPast) {
		t.Errorf("past: %v", err)
	}
	if _, err := b.SetReminder(reminderAt("x", strings.Repeat("я", alarms.MaxLabelRunes), 7, 0)); err != nil {
		t.Errorf("80 runes: %v", err)
	}
	for i := range alarms.MaxReminders - 1 {
		if _, err := b.SetReminder(reminderAt("r"+string(rune('a'+i)), "a", 7, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.SetReminder(reminderAt("full", "a", 7, 0)); !errors.Is(err, alarms.ErrFull) {
		t.Fatalf("21st: %v", err)
	}
}

func TestABookWrittenBeforeRemindersLoadsAndOneWithThemRoundTrips(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"alarms":[{"id":"a1","time":"07:00","days":["mon"],"level":50}],"timers":[]}`
	if err := os.WriteFile(f.path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	b := f.open(at(26, 6, 0, 0))
	if len(alarmIDs(b)) != 1 || len(b.Reminders()) != 0 {
		t.Fatal("the old book did not load")
	}
	if _, err := b.SetReminder(alarms.Reminder{ID: "in", Label: "чай", At: at(26, 6, 5, 0), SoundID: "s9"}); err != nil {
		t.Fatal(err)
	}
	a := alarm("a2", 8, 0, alarms.EveryDay)
	a.SoundID = "m1"
	f.mustSetAlarm(b, at(26, 6, 0, 0), a)
	b = f.open(at(26, 6, 1, 0))
	list, _ := b.List()
	got := b.Reminders()
	if len(got) != 1 || got[0].SoundID != "s9" || !got[0].At.Equal(at(26, 6, 5, 0)) || list[1].SoundID != "m1" {
		t.Fatalf("round trip %+v %+v", got, list)
	}
	if len(f.logs.events("alarms_store_invalid")) != 0 {
		t.Fatal("store invalid")
	}
}

func TestSetReminderWithoutALevelNeedsNoSessionAndWithOneAWokenOne(t *testing.T) {
	c := newCapRig(t, at(26, 6, 0, 0))
	if err := c.apply(`{"timeZone":"Asia/Bishkek"}`); err != nil {
		t.Fatal(err)
	}
	c.want("set_reminder", `{"id":"r1","label":"чайник","inSeconds":600}`,
		`{"reminder":{"id":"r1","at":"2026-10-26T00:10:00Z","label":"чайник","next":"2026-10-26T00:10:00Z"}}`, "")
	c.want("set_reminder", `{"sessionId":"followup","id":"r2","label":"a","time":"09:30","days":["mon"],"soundId":"s1"}`,
		`{"reminder":{"id":"r2","time":"09:30","days":["mon"],"label":"a","soundId":"s1","next":"2026-10-26T03:30:00Z"}}`, "")
	c.want("set_reminder", `{"sessionId":"followup","id":"r3","label":"a","time":"09:30","level":30}`, "", "follow_up")
	c.want("set_reminder", `{"id":"r3","label":"a","time":"09:30","level":30}`, "", "wake_required")
	c.want("set_reminder", `{"sessionId":"woken","id":"r3","label":"a","time":"09:30","level":30}`,
		`{"reminder":{"id":"r3","time":"09:30","date":"2026-10-26","label":"a","level":30,"next":"2026-10-26T03:30:00Z"}}`, "")
	for _, bad := range []string{
		`{"id":"x","label":"a"}`,
		`{"id":"x","label":"a","time":"09:30","inSeconds":5}`,
		`{"id":"x","label":"a","inSeconds":5,"days":["mon"]}`,
		`{"id":"x","label":"","inSeconds":5}`,
		`{"id":"x","label":"a","inSeconds":0}`,
		`{"id":"x","label":"a","inSeconds":5,"level":0}`,
	} {
		c.want("set_reminder", bad, "", "invalid")
	}
	c.want("cancel_reminder", `{"id":"r1"}`, `{"cancelled":["r1"]}`, "")
	c.want("cancel_reminder", `{"id":"r1"}`, "", "not_found")
	c.want("cancel_reminder", `{"all":true}`, `{"cancelled":["r2","r3"]}`, "")
	var st struct {
		Reminders []json.RawMessage `json:"reminders"`
	}
	if err := json.Unmarshal([]byte(c.latest()), &st); err != nil || len(st.Reminders) != 0 {
		t.Fatalf("state %v %v", st, err)
	}
}

// ringSpeaker takes rings of segments too
type ringSpeaker struct {
	speaker
	mu     sync.Mutex
	starts []*audiodv1.ToneStart
}

func (s *ringSpeaker) StartRing(start *audiodv1.ToneStart) error {
	s.calls.add("ring_start %s %s", start.GetId(), strings.TrimPrefix(start.GetKind().String(), "TONE_KIND_"))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts = append(s.starts, start)
	return nil
}

func (s *ringSpeaker) last() *audiodv1.ToneStart {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts[len(s.starts)-1]
}

type fakeSounds struct{}

func (fakeSounds) RingSound(id string) (string, string, bool) {
	return "https://server.test/v2/ring-sound/" + id, "Bearer tok", true
}

func segments(list []*audiodv1.RingSegment) string {
	var out []string
	for _, s := range list {
		switch {
		case s.GetUrl() != "":
			out = append(out, "url:"+s.GetUrl())
		case s.GetPromptId() != "":
			out = append(out, "prompt:"+s.GetLang()+"/"+s.GetPromptId())
		case s.GetSilenceMs() > 0:
			out = append(out, "silence")
		default:
			out = append(out, strings.TrimPrefix(s.GetTone().String(), "TONE_KIND_"))
		}
	}
	return strings.Join(out, ",")
}

func TestARingsSegmentsStreamItsSoundOverItsFallback(t *testing.T) {
	g := newRig(t, at(26, 6, 0, 0), 30)
	rs := &ringSpeaker{speaker: speaker{calls: g.calls}}
	g.r.UseSpeaker(rs)
	g.r.UseLanguage(func() string { return "en" })

	// a reminder without its sound: the chime, the bundled prompt and a pause, looped; nothing touches the volume
	if _, err := g.book.SetReminder(alarms.Reminder{ID: "r1", Label: "a", At: at(26, 6, 1, 0)}); err != nil {
		t.Fatal(err)
	}
	g.step(at(26, 6, 1, 0))
	g.want("ring", "end_session", "ringing true", "ring_start ring-1 REMINDER")
	start := rs.last()
	if segments(start.GetSegments()) != "REMINDER,prompt:en/reminder,silence" || !start.GetLoop() ||
		len(start.GetFallback()) != 0 || start.GetUrlAuthorization() != "" {
		t.Fatalf("plain reminder %v", start)
	}
	g.sound("ring-1", at(26, 6, 1, 1))
	g.step(at(26, 6, 1, 30))
	g.r.Stop(alarms.StopButton)
	g.want("stop", "tone_stop ring-1", "ringing false")

	// with its speech on the server: the chime, the speech and a pause, over the bundled one
	g.r.UseSounds(fakeSounds{})
	if _, err := g.book.SetReminder(alarms.Reminder{ID: "r2", Label: "a", At: at(26, 6, 2, 0), SoundID: "sp1"}); err != nil {
		t.Fatal(err)
	}
	g.step(at(26, 6, 2, 0))
	g.calls.take()
	start = rs.last()
	if segments(start.GetSegments()) != "REMINDER,url:https://server.test/v2/ring-sound/sp1,silence" ||
		segments(start.GetFallback()) != "REMINDER,prompt:en/reminder,silence" || start.GetUrlAuthorization() != "Bearer tok" {
		t.Fatalf("spoken reminder %v", start)
	}
	g.r.ToneState(&audiodv1.ToneState{Id: start.GetId(), Sounding: true})
	g.r.ToneState(&audiodv1.ToneState{Id: start.GetId(), Sounding: true, Fallback: true, Code: "timeout"})
	g.step(at(26, 6, 2, 5))
	if ev := g.logs.events("ring_fallback"); len(ev) != 1 || ev[0]["code"] != "timeout" || ev[0]["kind"] != "reminder" {
		t.Fatalf("fallback %v", ev)
	}
	g.r.Stop(alarms.StopButton)
	g.calls.take()

	// an alarm with its music: the url looped over its own tone
	a := alarm("a1", 6, 5, alarms.EveryDay)
	a.SoundID = "m1"
	g.mustSetAlarm(g.book, at(26, 6, 3, 0), a)
	g.step(at(26, 6, 5, 0))
	start = rs.last()
	if start.GetKind() != audiodv1.ToneKind_TONE_KIND_ALARM ||
		segments(start.GetSegments()) != "url:https://server.test/v2/ring-sound/m1" || len(start.GetFallback()) != 0 {
		t.Fatalf("music alarm %v", start)
	}
	g.r.Stop(alarms.StopButton)
	g.calls.take()

	// an alarm without music is the tone alone
	g.mustSetAlarm(g.book, at(26, 6, 5, 30), alarm("a2", 6, 6, alarms.EveryDay))
	g.step(at(26, 6, 6, 0))
	if got := g.calls.take(); len(got) == 0 || !strings.HasPrefix(got[len(got)-1], "tone_start ") {
		t.Fatalf("plain alarm %v", got)
	}
	if g.logs.contains("tok") || g.logs.contains("ring-sound") {
		t.Fatal("the url or the token was logged")
	}
}

// the server's authenticated API (Home Assistant, a screen's press) sets and cancels timers and reminders without a
// level with no session; alarms and a reminder with a level keep theirs
func TestTimersAndReminderWithoutALevelNeedNoSession(t *testing.T) {
	c := newCapRig(t, at(26, 6, 0, 0))
	if err := c.apply(`{"timeZone":"Asia/Bishkek"}`); err != nil {
		t.Fatal(err)
	}
	c.want("set_timer", `{"id":"tea","seconds":60}`,
		`{"timer":{"id":"tea","seconds":60,"endsAt":"2026-10-26T00:01:00Z","next":"2026-10-26T00:01:00Z"}}`, "")
	c.want("cancel_timer", `{"id":"tea"}`, `{"cancelled":["tea"]}`, "")
	c.want("cancel_timer", `{}`, "", "not_found")
	c.want("set_reminder", `{"id":"r1","label":"a","time":"09:00"}`,
		`{"reminder":{"id":"r1","time":"09:00","date":"2026-10-26","label":"a","next":"2026-10-26T03:00:00Z"}}`, "")
	c.want("set_reminder", `{"id":"r2","label":"a","time":"09:00","level":20}`, "", "wake_required")
	c.want("set_alarm", `{"id":"a1","time":"09:00"}`, "", "invalid")
	c.want("cancel_alarm", `{"all":true}`, "", "invalid")
	// a named session is checked as before: one the device does not know, or the server's own, is refused
	c.want("set_timer", `{"sessionId":"nobody","id":"tea","seconds":60}`, "", "wake_required")
	c.want("set_reminder", `{"sessionId":"event","id":"r3","label":"a","time":"09:00"}`, "", "wake_required")
	c.want("cancel_reminder", `{"sessionId":"nobody","id":"r1"}`, "", "wake_required")
	c.want("cancel_reminder", `{"id":"r1"}`, `{"cancelled":["r1"]}`, "")
}
