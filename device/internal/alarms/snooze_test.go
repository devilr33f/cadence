package alarms_test

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

// a snooze of a repeating alarm rings it again at its own level, once, and leaves its days alone: the next morning
// rings as set. it outlives a restart
func TestASnoozedAlarmRingsAgainAndKeepsItsSchedule(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	g.mustSetAlarm(g.book, at(26, 6, 59, 0), alarms.Alarm{ID: "wake", Hour: 7, Days: weekdays, Level: 60})
	start := at(26, 7, 0, 0)
	g.step(start)
	g.sound("ring-1", start.Add(time.Second))
	g.calls.take()

	s, err := g.r.Snooze("", "", 5)
	if err != nil || s.Kind != alarms.KindAlarm || s.ID != "wake" || !s.Until.Equal(start.Add(5*time.Minute+time.Second)) {
		t.Fatalf("snooze = %+v, %v", s, err)
	}
	g.want("the snooze stops the ring", "tone_stop ring-1", "ringing false")
	g.wantEvents("ring_stop", "alarm wake snooze")
	if g.r.Ringing() {
		t.Fatal("ringing after a snooze")
	}
	if alarmIDs(g.book) == nil {
		t.Fatal("the snooze took the alarm away")
	}

	// a restart keeps the snooze
	reopened := g.open(start.Add(time.Minute))
	if got := reopened.Snoozed(); len(got) != 1 || got[0].ID != "wake" || got[0].Level != 60 {
		t.Fatalf("snoozed after a restart %+v", got)
	}

	again := start.Add(5*time.Minute + time.Second)
	g.step(again.Add(-time.Second))
	g.want("before the snooze ends")
	g.step(again)
	g.want("the snooze ends", "end_session", "ringing true", "tone_start ring-2 ALARM")
	g.sound("ring-2", again)
	g.step(again.Add(30 * time.Second))
	g.want("its own level, by the ramp", "transient 60")
	g.r.StopRing(input.Action)
	g.calls.take()
	if len(g.book.Snoozed()) != 0 {
		t.Fatal("a snooze that rang is still kept")
	}
	if next, ok := g.book.NextOf(alarms.Alarm{ID: "wake", Hour: 7, Days: weekdays, Level: 60}); !ok || !next.Equal(at(27, 7, 0, 0)) {
		t.Fatalf("next %v %t, want tomorrow 07:00", next, ok)
	}
}

// a one-time alarm is gone once it rang; its snooze rings it once more all the same, and a cancel of its id takes the
// snooze back
func TestAOneTimeAlarmSnoozesAndACancelTakesTheSnooze(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	g.mustSetAlarm(g.book, at(26, 6, 59, 0), dated("once", 7, 0, date(2026, 10, 26)))
	g.step(at(26, 7, 0, 0))
	g.r.StopRing(input.Action)
	g.calls.take()
	if len(alarmIDs(g.book)) != 0 {
		t.Fatal("a one-time alarm stayed after its ring")
	}
	g.clock.set(at(26, 7, 3, 0))
	if _, err := g.r.Snooze(alarms.KindAlarm, "once", 0); err != nil {
		t.Fatalf("snooze of the stopped ring: %v", err)
	}
	if got := g.book.Snoozed(); len(got) != 1 || !got[0].Until.Equal(at(26, 7, 12, 0)) {
		t.Fatalf("snoozed %+v, want the config's 9 minutes", got)
	}
	if !g.book.CancelAlarm("once") {
		t.Fatal("the cancel found nothing")
	}
	if len(g.book.Snoozed()) != 0 {
		t.Fatal("the cancel left the snooze")
	}
	g.step(at(26, 7, 12, 0))
	g.want("nothing after the cancel")
}

// a snooze without a ring that sounds finds the one stopped in the last 10 minutes, and nothing after that
func TestASnoozeFindsTheLastRingWithinItsWindow(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	g.mustSetTimer(g.book, at(26, 6, 59, 0), "tea", 60)
	g.step(at(26, 7, 0, 0))
	g.r.Stop(alarms.StopWake)
	if l, ok := g.book.Last(); !ok || l.ID != "tea" || l.StoppedBy != alarms.StopWake {
		t.Fatalf("last %+v %t", l, ok)
	}
	g.clock.set(at(26, 7, 10, 1))
	if _, err := g.r.Snooze("", "", 5); !errors.Is(err, alarms.ErrNothing) {
		t.Fatalf("snooze past the window: %v", err)
	}
	g.clock.set(at(26, 7, 10, 0))
	if _, err := g.r.Snooze(alarms.KindAlarm, "", 5); !errors.Is(err, alarms.ErrNothing) {
		t.Fatalf("snooze of another kind: %v", err)
	}
	s, err := g.r.Snooze(alarms.KindTimer, "tea", 5)
	if err != nil || !s.Until.Equal(at(26, 7, 15, 0)) {
		t.Fatalf("snooze = %+v, %v", s, err)
	}
	g.calls.take()
	g.step(at(26, 7, 15, 0))
	g.want("the timer again", "end_session", "ringing true", "tone_start ring-2 TIMER")
}

// the config's ramp and limit, and an alarm's own over them: a 2-minute ramp keeps the 5-point steps, a ramp of 0 is
// the level at once, an alarm's limit ends it
func TestRampAndLimitFromTheConfigAndTheAlarm(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	s := alarms.DefaultSettings()
	s.Ramp, s.AlarmLimit = 2*time.Minute, 5*time.Minute
	g.book.SetSettings(s)
	g.mustSetAlarm(g.book, at(26, 6, 59, 0), alarms.Alarm{ID: "slow", Hour: 7, Days: weekdays, Level: 50})
	start := at(26, 7, 0, 0)
	g.step(start)
	g.sound("ring-1", start)
	g.calls.take()
	// 20 points over 120 s: a step each 30 s
	g.step(start.Add(29 * time.Second))
	g.want("29 s")
	g.step(start.Add(30 * time.Second))
	g.want("30 s", "transient 35")
	g.step(start.Add(2 * time.Minute))
	g.want("2 min", "transient 50")
	g.step(start.Add(5 * time.Minute))
	g.want("the config's limit", "tone_stop ring-1", "plain 30", "ringing false")

	g.mustSetAlarm(g.book, at(26, 7, 6, 0), alarms.Alarm{ID: "now", Hour: 7, Minute: 7, Days: weekdays, Level: 70,
		Options: alarms.RingOptions{HasRamp: true, RampSeconds: 0, LimitSeconds: 60}})
	start = at(26, 7, 7, 0)
	g.step(start)
	g.sound("ring-2", start)
	g.want("a ramp of 0", "end_session", "ringing true", "tone_start ring-2 ALARM", "transient 70")
	g.step(start.Add(time.Minute))
	g.want("its own limit", "tone_stop ring-2", "plain 30", "ringing false")
}

// a quiet ramp starts at level 5 right behind its tone, below the level in force, then climbs to the alarm's level
// and never above it; the stop puts the level in force back once
func TestAQuietRampStartsLowAndClimbs(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	g.mustSetAlarm(g.book, at(26, 6, 59, 0), alarms.Alarm{ID: "soft", Hour: 7, Days: weekdays, Level: 40,
		Options: alarms.RingOptions{RampFrom: alarms.RampQuiet}})
	start := at(26, 7, 0, 0)
	g.step(start)
	g.want("quiet from the first ask", "end_session", "ringing true", "tone_start ring-1 ALARM", "transient 5")
	g.sound("ring-1", start.Add(time.Second))
	// 35 points over 30 s: a step each 30*5/35 s
	g.step(start.Add(5 * time.Second))
	g.want("5 s", "transient 10")
	g.step(start.Add(20 * time.Second))
	g.want("20 s, still below the level in force", "transient 25")
	g.step(start.Add(30 * time.Second))
	g.want("30 s", "transient 40")
	g.step(start.Add(time.Minute))
	g.want("never above")
	g.wantLevel(30)
	g.r.StopRing(input.Action)
	g.want("the stop", "tone_stop ring-1", "plain 30", "ringing false")

	// below 5 already: it starts there; an alarm quieter than that rings at its own level
	if _, err := g.vol.Set(3, volume.OriginButton); err != nil {
		t.Fatal(err)
	}
	g.calls.take()
	g.mustSetAlarm(g.book, at(26, 7, 1, 0), alarms.Alarm{ID: "tiny", Hour: 7, Minute: 2, Days: weekdays, Level: 2,
		Options: alarms.RingOptions{RampFrom: alarms.RampQuiet}})
	g.step(at(26, 7, 2, 0))
	g.want("an alarm below the level in force", "end_session", "ringing true", "tone_start ring-2 ALARM", "transient 2")
}

// an alarm nobody stops rings again snoozeMinutes later, autoSnooze times, then no more
func TestAutoSnooze(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	s := alarms.DefaultSettings()
	s.AlarmLimit, s.SnoozeMinutes, s.AutoSnooze = time.Minute, 2, 1
	g.book.SetSettings(s)
	g.mustSetAlarm(g.book, at(26, 6, 59, 0), dated("once", 7, 0, date(2026, 10, 26)))
	start := at(26, 7, 0, 0)
	g.step(start)
	g.step(start.Add(time.Minute))
	if got := g.book.Snoozed(); len(got) != 1 || !got[0].Until.Equal(start.Add(3*time.Minute)) || got[0].AutoSnoozes != 1 {
		t.Fatalf("snoozed %+v", got)
	}
	g.calls.take()
	g.step(start.Add(3 * time.Minute))
	g.want("again", "end_session", "ringing true", "tone_start ring-2 ALARM")
	g.step(start.Add(4 * time.Minute))
	if got := g.book.Snoozed(); len(got) != 0 {
		t.Fatalf("snoozed past autoSnooze %+v", got)
	}
	if n := len(g.logs.events("ring_timeout")); n != 2 {
		t.Fatalf("ring_timeout lines %d", n)
	}
}

// a stop_ring of a ring that waits drops it; one that sounds stops; nothing is not_found
func TestStopForTakesTheNamedRing(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	g.mustSetTimer(g.book, at(26, 6, 59, 0), "a", 60)
	g.mustSetTimer(g.book, at(26, 6, 59, 0), "b", 60)
	g.step(at(26, 7, 0, 0))
	if r, ok := g.r.StopFor(alarms.KindTimer, "b"); !ok || r.ID != "b" {
		t.Fatalf("stop of the waiting one %+v %t", r, ok)
	}
	if r, ok := g.r.StopFor("", ""); !ok || r.ID != "a" {
		t.Fatalf("stop of the sounding one %+v %t", r, ok)
	}
	if _, ok := g.r.StopFor("", ""); ok {
		t.Fatal("a stop with nothing ringing")
	}
	g.wantEvents("ring_stop", "timer b remote", "timer a remote")
}

// the action button held down while it stops a ring snoozes that ring, offline, by the config's minutes
func TestSnoozeStoppedSnoozesOnlyAButtonStop(t *testing.T) {
	g := newRig(t, at(26, 6, 59, 0), 30)
	g.mustSetTimer(g.book, at(26, 6, 59, 0), "tea", 60)
	g.step(at(26, 7, 0, 0))
	g.r.Stop(alarms.StopWord)
	g.r.SnoozeStopped()
	if len(g.book.Snoozed()) != 0 {
		t.Fatal("a word's stop snoozed")
	}
	g.mustSetTimer(g.book, at(26, 7, 0, 0), "egg", 60)
	g.step(at(26, 7, 1, 0))
	g.r.StopRing(input.Action)
	g.r.SnoozeStopped()
	if got := g.book.Snoozed(); len(got) != 1 || got[0].ID != "egg" || !got[0].Until.Equal(at(26, 7, 10, 0)) {
		t.Fatalf("snoozed %+v", got)
	}
}

// a file written before snoozes and ring options loads as it was; one with them reads back the same
func TestTheStoreReadsOldFilesAndKeepsSnoozes(t *testing.T) {
	f := newFixture(t)
	old := fmt.Sprintf(`{"alarms":[{"id":"wk","time":"07:00","days":["mon"],"level":50,"lastRungAt":%d}],`+
		`"timers":[{"id":"tea","seconds":300,"endsAt":%d}]}`, at(19, 7, 0, 0).Unix(), at(26, 6, 5, 0).Unix())
	if err := os.MkdirAll(strings.TrimSuffix(f.path, "/alarms"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	b := f.open(at(26, 6, 0, 0))
	wantIDs(t, "alarms", alarmIDs(b), "wk")
	wantIDs(t, "timers", timerIDs(b), "tea")
	if len(b.Snoozed()) != 0 {
		t.Fatal("an old file has snoozes")
	}
	if _, ok := b.Last(); ok {
		t.Fatal("an old file has a last ring")
	}

	f.mustSetAlarm(b, at(26, 6, 0, 0), alarms.Alarm{ID: "soft", Hour: 8, Days: weekdays, Level: 40,
		Options: alarms.RingOptions{HasRamp: true, RampSeconds: 300, RampFrom: alarms.RampQuiet, LimitSeconds: 900}})
	if _, err := b.SnoozeRing(alarms.Ring{Kind: alarms.KindAlarm, ID: "soft", Level: 40, AutoSnoozes: 1,
		Options: alarms.RingOptions{HasRamp: true, RampSeconds: 300}}, 7); err != nil {
		t.Fatal(err)
	}
	b.Stopped(alarms.Ring{Kind: alarms.KindTimer, ID: "egg"}, alarms.StopRemote, at(26, 6, 0, 0))
	back := f.open(at(26, 6, 0, 0))
	list, _ := back.List()
	if fmt.Sprint(list[1].Options) != fmt.Sprint(alarms.RingOptions{HasRamp: true, RampSeconds: 300, RampFrom: alarms.RampQuiet, LimitSeconds: 900}) {
		t.Errorf("options back %+v", list[1].Options)
	}
	if got := back.Snoozed(); len(got) != 1 || got[0].AutoSnoozes != 1 || got[0].Options.RampSeconds != 300 ||
		!got[0].Until.Equal(at(26, 6, 7, 0)) {
		t.Errorf("snoozed back %+v", got)
	}
	if l, ok := back.Last(); !ok || l.ID != "egg" || l.StoppedBy != alarms.StopRemote {
		t.Errorf("last back %+v %t", l, ok)
	}
	if len(f.logs.events("alarms_store_invalid")) != 0 {
		t.Error("a file the book wrote read as invalid")
	}
}

// the config takes the alarms setting's keys within their bounds over the defaults; anything else leaves it as it was
func TestTheConfigSetsRampLimitsAndSnooze(t *testing.T) {
	c := newCapRig(t, at(26, 6, 0, 0))
	if err := c.apply(`{"timeZone":"Asia/Bishkek","rampSeconds":120,"rampFrom":"quiet","alarmLimitSeconds":900,` +
		`"timerLimitSeconds":30,"reminderLimitSeconds":45,"snoozeMinutes":5,"autoSnooze":2}`); err != nil {
		t.Fatal(err)
	}
	want := alarms.Settings{Ramp: 2 * time.Minute, RampFrom: alarms.RampQuiet, AlarmLimit: 15 * time.Minute,
		TimerLimit: 30 * time.Second, ReminderLimit: 45 * time.Second, SnoozeMinutes: 5, AutoSnooze: 2}
	if got := c.book.Settings(); got != want {
		t.Fatalf("settings %+v", got)
	}
	for _, bad := range []string{
		`{"timeZone":"UTC","rampSeconds":1801}`, `{"timeZone":"UTC","rampFrom":"loud"}`,
		`{"timeZone":"UTC","alarmLimitSeconds":59}`, `{"timeZone":"UTC","timerLimitSeconds":601}`,
		`{"timeZone":"UTC","reminderLimitSeconds":9}`, `{"timeZone":"UTC","snoozeMinutes":0}`,
		`{"timeZone":"UTC","autoSnooze":6}`, `{"timeZone":"UTC","snoozeMinutes":null}`, `{"timeZone":"UTC","other":1}`,
	} {
		if err := c.apply(bad); err == nil {
			t.Errorf("apply %s took", bad)
		}
	}
	if got := c.book.Settings(); got != want {
		t.Fatalf("a refused config changed the settings %+v", got)
	}
	if err := c.apply(`{"timeZone":"Asia/Bishkek"}`); err != nil || c.book.Settings() != alarms.DefaultSettings() {
		t.Fatalf("a zone alone is the defaults: %v %+v", err, c.book.Settings())
	}
}

// snooze and stop_ring need no session (owner, 2026-11-16); set_alarm carries an alarm's own ramp and limit
func TestSnoozeAndStopRingCommands(t *testing.T) {
	c := newCapRig(t, at(26, 6, 59, 0))
	if err := c.apply(`{"timeZone":"Asia/Bishkek"}`); err != nil {
		t.Fatal(err)
	}
	c.want("set_alarm", `{"sessionId":"woken","id":"soft","time":"07:00","days":["mon"],"level":40,"rampSeconds":300,"rampFrom":"quiet","limitSeconds":900}`,
		`{"alarm":{"id":"soft","time":"07:00","days":["mon"],"level":40,"rampSeconds":300,"rampFrom":"quiet","limitSeconds":900},"replaced":false}`, "")
	for _, bad := range []string{
		`{"sessionId":"woken","id":"x","time":"07:00","rampSeconds":-1}`,
		`{"sessionId":"woken","id":"x","time":"07:00","rampFrom":"loud"}`,
		`{"sessionId":"woken","id":"x","time":"07:00","limitSeconds":30}`,
	} {
		c.want("set_alarm", bad, "", "invalid")
	}
	c.want("snooze", `{}`, "", "not_found")
	c.want("stop_ring", `{}`, "", "not_found")
	c.want("snooze", `{"minutes":61}`, "", "invalid")
	c.want("snooze", `{"kind":"song"}`, "", "invalid")
	c.want("snooze", `{"sessionId":""}`, "", "invalid")

	c.clock.set(at(26, 7, 0, 0))
	alarms.Step(c.r, at(26, 7, 0, 0))
	if !c.r.Ringing() {
		t.Fatal("not ringing")
	}
	c.want("snooze", `{"sessionId":"anything","minutes":10}`,
		`{"snoozed":{"kind":"alarm","id":"soft","until":"2026-10-26T01:10:00Z"}}`, "")
	if c.r.Ringing() {
		t.Fatal("ringing after the snooze")
	}
	c.clock.set(at(26, 7, 10, 0))
	alarms.Step(c.r, at(26, 7, 10, 0))
	c.want("stop_ring", `{"kind":"alarm","id":"soft"}`, `{"stopped":{"kind":"alarm","id":"soft"}}`, "")
	// the stopped ring is still there to snooze for 10 minutes
	c.want("snooze", `{"kind":"alarm"}`, `{"snoozed":{"kind":"alarm","id":"soft","until":"2026-10-26T01:19:00Z"}}`, "")
	s := c.latest()
	if !strings.Contains(s, `"snoozed":[{"kind":"alarm","id":"soft","until":"2026-10-26T01:19:00Z"}]`) ||
		!strings.Contains(s, `"lastRing":{"kind":"alarm","id":"soft","at":"2026-10-26T01:10:00Z","stoppedBy":"remote"}`) ||
		!strings.Contains(s, `"next":"2026-10-26T01:19:00Z"`) {
		t.Fatalf("state %s", s)
	}
	checkSchemas(t, c, s)
}

// every schema compiles, and the state the capability offers passes its own
func checkSchemas(t *testing.T, c *capRig, state string) {
	t.Helper()
	d := c.p.Descriptor()
	compileSchema := func(src string) *jsonschema.Schema {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		comp := jsonschema.NewCompiler()
		comp.DefaultDraft(jsonschema.Draft2020)
		if err := comp.AddResource("s.json", doc); err != nil {
			t.Fatal(err)
		}
		s, err := comp.Compile("s.json")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	compileSchema(d.ConfigSchema)
	for _, s := range d.Commands {
		compileSchema(s)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(state))
	if err != nil {
		t.Fatal(err)
	}
	if err := compileSchema(d.StateSchema).Validate(doc); err != nil {
		t.Fatalf("state against its schema: %v", err)
	}
}
