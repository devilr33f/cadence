package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/ringsounds"
)

// fakeSounds speaks every text into one sound, or fails
type fakeSounds struct {
	spoken []string
	fail   bool
	info   map[string]ringsounds.Info
}

func (s *fakeSounds) Speech(_ context.Context, deviceID, text string) (ringsounds.Sound, error) {
	if deviceID != "d1" {
		panic("wrong device")
	}
	s.spoken = append(s.spoken, text)
	if s.fail {
		return ringsounds.Sound{}, ringsounds.ErrTTS
	}
	return ringsounds.Sound{ID: "snd0000000000001", Kind: ringsounds.KindSpeech, Title: text}, nil
}

func (s *fakeSounds) Info(_ context.Context, _ string, ids []string) map[string]ringsounds.Info {
	out := map[string]ringsounds.Info{}
	for _, id := range ids {
		if i, ok := s.info[id]; ok {
			out[id] = i
		}
	}
	return out
}

const (
	noReminders  = `{"alarms":[],"timers":[],"reminders":[],"ringing":null}`
	twoReminders = `{"alarms":[],"timers":[],"ringing":null,"reminders":[` +
		`{"id":"m1","time":"09:00","days":["mon","tue","wed","thu","fri","sat","sun"],"label":"Таблетки","soundId":"snd1"},` +
		`{"id":"m2","at":"2026-11-11T09:20:00Z","label":"Напоминаю: выключить духовку"}]}`
)

func TestReminderSetSpeaksItsTextThenSetsItOnTheDevice(t *testing.T) {
	f := &alarmsFake{state: noReminders, answer: map[string]string{
		"set_reminder": `{"reminder":{"id":"x","at":"2026-11-11T09:20:00Z","label":"Напоминаю: выключить духовку","soundId":"snd0000000000001"}}`,
	}}
	sounds := &fakeSounds{}
	d := f.deps()
	d.Sounds = sounds
	// a reminder without a level goes in a follow-up too: it never touches the volume
	out, e := run(t, tools.Reminder(d), cadencev2.Trigger_TRIGGER_FOLLOW_UP,
		`{"action":"set","text":"Напоминаю: выключить духовку","inMinutes":20}`)
	if e != nil {
		t.Fatal(e)
	}
	f.wantCalls(t, `set_reminder {"sessionId":"s1","id":"<id>","inSeconds":1200,"label":"Напоминаю: выключить духовку","soundId":"snd0000000000001"}`)
	if string(out) != `{"reminder":{"text":"Напоминаю: выключить духовку","inMinutes":20},"spoken":true}` {
		t.Fatalf("out %s", out)
	}
	if len(sounds.spoken) != 1 || strings.Contains(f.logs.String(), "духовк") {
		t.Fatalf("spoken %v, logs %s", sounds.spoken, f.logs.String())
	}

	// the speech failed: set anyway, said so
	sounds.fail = true
	f.answer["set_reminder"] = `{"reminder":{"id":"x","time":"09:00","days":["mon"],"label":"a"}}`
	out, e = run(t, tools.Reminder(d), wake, `{"action":"set","text":"a","time":"9:00","days":["mon"],"level":30}`)
	if e != nil {
		t.Fatal(e)
	}
	f.wantCalls(t, `set_reminder {"sessionId":"s1","id":"<id>","time":"09:00","days":["mon"],"label":"a","level":30}`)
	if !strings.Contains(string(out), `"spoken":false`) {
		t.Fatalf("out %s", out)
	}

	// a level outside a woken session is rule 7's refusal, with nothing made or sent
	sounds.spoken = nil
	if _, e := run(t, tools.Reminder(d), cadencev2.Trigger_TRIGGER_FOLLOW_UP,
		`{"action":"set","text":"a","inMinutes":5,"level":30}`); !wakeRefusal(e) {
		t.Fatalf("level in a follow-up: %+v", e)
	}
	f.wantCalls(t)
	if len(sounds.spoken) != 0 {
		t.Fatal("spoken before the refusal")
	}
}

func TestReminderArgs(t *testing.T) {
	for _, args := range []string{
		`{"action":"set","inMinutes":5}`, `{"action":"set","text":"a"}`,
		`{"action":"set","text":"a","inMinutes":5,"time":"9:00"}`,
		`{"action":"set","text":"a","inMinutes":5,"days":["mon"]}`,
		`{"action":"set","text":"a","inMinutes":0}`, `{"action":"set","text":"a","inMinutes":10081}`,
		`{"action":"set","text":"` + strings.Repeat("я", 301) + `","inMinutes":5}`,
		`{"action":"set","text":"a","time":"9:00","date":"2026-11-12","days":["mon"]}`,
		`{"action":"cancel","all":true,"text":"a"}`, `{"action":"list","text":"a"}`,
		`{"action":"set","text":"a","inMinutes":5,"minutes":5}`,
	} {
		f := &alarmsFake{state: noReminders}
		if _, e := run(t, tools.Reminder(f.deps()), wake, args); e == nil || e.Code != "invalid_arguments" {
			t.Fatalf("%s: %+v", args, e)
		}
		f.wantCalls(t)
	}
}

func TestReminderListAndCancelByItsWords(t *testing.T) {
	f := &alarmsFake{state: twoReminders, answer: map[string]string{"cancel_reminder": `{"cancelled":["m2"]}`}}
	d := f.deps()
	d.Sounds = &fakeSounds{info: map[string]ringsounds.Info{"snd1": {Kind: ringsounds.KindSpeech, Title: "Напоминаю: выпить таблетки"}}}
	out, e := run(t, tools.Reminder(d), wake, `{"action":"list"}`)
	if e != nil {
		t.Fatal(e)
	}
	if string(out) != `{"reminders":[{"text":"Напоминаю: выпить таблетки","time":"09:00","days":["mon","tue","wed","thu","fri","sat","sun"]},`+
		`{"text":"Напоминаю: выключить духовку","inMinutes":20}]}` {
		t.Fatalf("list %s", out)
	}
	out, e = run(t, tools.Reminder(d), cadencev2.Trigger_TRIGGER_FOLLOW_UP, `{"action":"cancel","text":"ДУХОВКУ"}`)
	if e != nil {
		t.Fatal(e)
	}
	f.wantCalls(t, `cancel_reminder {"sessionId":"s1","id":"<id>"}`)
	if f.ids[0] != "m2" || !strings.Contains(string(out), "духовку") {
		t.Fatalf("cancelled %v %s", f.ids, out)
	}
	// both match «напоминаю»: ask which
	if _, e := run(t, tools.Reminder(d), wake, `{"action":"cancel","text":"напоминаю"}`); e == nil || e.Code != "ambiguous" || len(e.Candidates) != 2 {
		t.Fatalf("ambiguous: %+v", e)
	}
	if _, e := run(t, tools.Reminder(d), wake, `{"action":"cancel","text":"кошка"}`); e == nil || e.Code != "not_found" {
		t.Fatalf("none: %+v", e)
	}
	f.wantCalls(t)
}

func TestReminderSnoozeAndADeviceWithoutReminders(t *testing.T) {
	f := &alarmsFake{state: noReminders, answer: map[string]string{"snooze": `{"snoozed":{"kind":"reminder","id":"m1","until":"2026-11-11T09:10:00Z"}}`}}
	out, e := run(t, tools.Reminder(f.deps()), cadencev2.Trigger_TRIGGER_FOLLOW_UP, `{"action":"snooze","minutes":10}`)
	if e != nil || string(out) != `{"snoozed":{"kind":"reminder","minutes":10}}` {
		t.Fatalf("snooze %s %+v", out, e)
	}
	f.wantCalls(t, `snooze {"sessionId":"s1","kind":"reminder","minutes":10}`)
	f.err = errors.New("x")
	f.state = ""
	if _, e := run(t, tools.Reminder(f.deps()), wake, `{"action":"list"}`); e == nil || e.Code != "unavailable" {
		t.Fatalf("no alarms capability: %+v", e)
	}
	if label := tools.ReminderLabel(strings.Repeat("слово ", 30)); len([]rune(label)) > 80 || !strings.HasSuffix(label, "…") {
		t.Fatalf("label %q", label)
	}
	_ = json.Valid
}
