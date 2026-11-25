package alarms_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// sessions is how the device's recent sessions began
type sessions map[string]cadencev2.Trigger

func (s sessions) Trigger(id string) (cadencev2.Trigger, bool) {
	t, ok := s[id]
	return t, ok
}

var recent = sessions{
	"woken": cadencev2.Trigger_TRIGGER_WAKE, "pressed": cadencev2.Trigger_TRIGGER_BUTTON,
	"followup": cadencev2.Trigger_TRIGGER_FOLLOW_UP, "event": cadencev2.Trigger_TRIGGER_EVENT,
}

type capRig struct {
	*fixture
	book *alarms.Book
	r    *alarms.Ringer
	p    capability.Provider
}

// newCapRig is the capability over a book that has no zone yet, at now
func newCapRig(t *testing.T, now time.Time) *capRig {
	f := newFixture(t)
	book := f.openUnzoned(now)
	vol := volume.NewSetter(volume.NewStore(t.TempDir()), 30, slog.New(slog.DiscardHandler))
	r := alarms.NewRinger(alarms.RingerOptions{Book: book, Volume: vol, Sessions: recent, Log: slog.New(slog.DiscardHandler), Now: f.clock.Now})
	return &capRig{fixture: f, book: book, r: r, p: r.Capability()}
}

func (c *capRig) apply(cfg string) error {
	return c.p.Apply(context.Background(), 1, json.RawMessage(cfg))
}

func (c *capRig) command(name, args string) (string, string) {
	c.t.Helper()
	res, err := c.p.Command(context.Background(), name, json.RawMessage(args))
	if err == nil {
		return string(res), ""
	}
	var ce *capability.CommandError
	if !errors.As(err, &ce) {
		c.t.Fatalf("%s %s: %v", name, args, err)
	}
	if res != nil {
		c.t.Errorf("%s %s: a result with the code %s", name, args, ce.Code)
	}
	return "", ce.Code
}

func (c *capRig) want(name, args, result, code string) {
	c.t.Helper()
	gotResult, gotCode := c.command(name, args)
	if gotResult != result || gotCode != code {
		c.t.Errorf("%s %s = %s %q, want %s %q", name, args, gotResult, gotCode, result, code)
	}
}

// latest is the state the capability offers now
func (c *capRig) latest() string {
	c.t.Helper()
	select {
	case s := <-c.p.State():
		return string(s)
	default:
		c.t.Fatal("no state")
		return ""
	}
}

func TestTheAlarmsCapabilityDescribesItself(t *testing.T) {
	c := newCapRig(t, at(26, 6, 0, 0))
	d := c.p.Descriptor()
	if d.ID != "alarms" || d.Version != 1 {
		t.Fatalf("descriptor %s v%d", d.ID, d.Version)
	}
	schemas := map[string]string{"config": d.ConfigSchema, "state": d.StateSchema}
	for _, name := range []string{"set_alarm", "cancel_alarm", "set_timer", "cancel_timer", "snooze", "stop_ring",
		"set_reminder", "cancel_reminder"} {
		s, ok := d.Commands[name]
		if !ok {
			t.Fatalf("no %s", name)
		}
		schemas[name] = s
	}
	if len(d.Commands) != 8 {
		t.Fatalf("commands %v", d.Commands)
	}
	for name, s := range schemas {
		var v map[string]any
		if err := json.Unmarshal([]byte(s), &v); err != nil || v["type"] != "object" {
			t.Errorf("%s schema %q: %v", name, s, err)
		}
	}
	// the level is 1-100, 50 when none is named
	if !strings.Contains(d.Commands["set_alarm"], `"level":{"type":"integer","minimum":1,"maximum":100,"default":50}`) {
		t.Errorf("set_alarm level: %s", d.Commands["set_alarm"])
	}
}

// the config is the zone the device's alarms ring by: the server's timeZone setting
func TestTheConfigSetsTheZone(t *testing.T) {
	c := newCapRig(t, at(26, 6, 0, 0))
	alarm := `{"sessionId":"woken","id":"wake","time":"07:00","days":["mon"]}`
	// until a zone is set the device cannot tell when an alarm rings
	c.want("set_alarm", alarm, "", "unavailable")
	for _, bad := range []string{``, `[]`, `{}`, `{"timeZone":""}`, `{"timeZone":"Mars/Olympus"}`, `{"timeZone":"Local"}`,
		`{"timeZone":7}`, `{"timeZone":null}`, `{"timeZone":"Asia/Bishkek","x":1}`, `{"timeZone":"Asia/Bishkek"} {}`} {
		var ae *capability.ApplyError
		if err := c.apply(bad); !errors.As(err, &ae) || ae.Code != "invalid" {
			t.Errorf("apply %q: %v", bad, err)
		}
	}
	c.want("set_alarm", alarm, "", "unavailable")
	if err := c.apply(`{"timeZone":"Asia/Bishkek"}`); err != nil {
		t.Fatal(err)
	}
	c.want("set_alarm", alarm, `{"alarm":{"id":"wake","time":"07:00","days":["mon"],"level":50},"replaced":false}`, "")
	// rings by Bishkek: 07:00 there is 01:00 UTC
	if rings := c.book.TakeDue(time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)); len(rings) != 1 || rings[0].ID != "wake" {
		t.Fatalf("rings %v", rings)
	}
}

// rule 7: an alarm raises the volume when it rings, so only a session the owner began with the wake word or a press
// sets one; a follow-up hears whoever talks after a reply. timers and cancels go in any session the device began
func TestOnlyAWokenSessionSetsAnAlarm(t *testing.T) {
	c := newCapRig(t, at(26, 6, 0, 0))
	if err := c.apply(`{"timeZone":"Asia/Bishkek"}`); err != nil {
		t.Fatal(err)
	}
	set := func(session string) string {
		return `{"sessionId":"` + session + `","id":"wake","time":"07:00","days":["mon","fri"],"level":80}`
	}
	c.want("set_alarm", set("followup"), "", "follow_up")
	for _, session := range []string{"event", "unknown"} {
		c.want("set_alarm", set(session), "", "wake_required")
	}
	if alarms, _ := c.book.List(); len(alarms) != 0 {
		t.Fatalf("set without a woken session: %v", alarms)
	}
	const wake = `{"alarm":{"id":"wake","time":"07:00","days":["mon","fri"],"level":80},"replaced":false}`
	c.want("set_alarm", set("woken"), wake, "")
	c.want("set_alarm", set("pressed"), strings.Replace(wake, "false", "true", 1), "")

	c.want("set_timer", `{"sessionId":"followup","id":"tea","seconds":300}`,
		`{"timer":{"id":"tea","seconds":300,"endsAt":"2026-10-26T00:05:00Z","next":"2026-10-26T00:05:00Z"}}`, "")
	c.want("cancel_timer", `{"sessionId":"followup","id":"tea"}`, `{"cancelled":["tea"]}`, "")
	c.want("cancel_alarm", `{"sessionId":"followup","id":"wake"}`, `{"cancelled":["wake"]}`, "")
	for _, session := range []string{"event", "unknown"} {
		c.want("set_timer", `{"sessionId":"`+session+`","id":"tea","seconds":300}`, "", "wake_required")
		c.want("cancel_alarm", `{"sessionId":"`+session+`","all":true}`, "", "wake_required")
		c.want("cancel_timer", `{"sessionId":"`+session+`","all":true}`, "", "wake_required")
	}
}

func TestSetAlarmTakesADateOrDaysAndSaysWhatTheBookKept(t *testing.T) {
	c := newCapRig(t, at(26, 6, 0, 0))
	if err := c.apply(`{"timeZone":"Asia/Bishkek"}`); err != nil {
		t.Fatal(err)
	}
	// a one-time alarm without a date is pinned to its next occurrence
	c.want("set_alarm", `{"sessionId":"woken","id":"once","time":"05:30"}`,
		`{"alarm":{"id":"once","time":"05:30","date":"2026-10-27","level":50},"replaced":false}`, "")
	c.want("set_alarm", `{"sessionId":"woken","id":"trip","time":"09:15","date":"2026-11-02","level":1}`,
		`{"alarm":{"id":"trip","time":"09:15","date":"2026-11-02","level":1},"replaced":false}`, "")
	// its twin under another id takes its place
	c.want("set_alarm", `{"sessionId":"woken","id":"again","time":"09:15","date":"2026-11-02","level":60}`,
		`{"alarm":{"id":"again","time":"09:15","date":"2026-11-02","level":60},"replaced":true}`, "")
	c.want("set_alarm", `{"sessionId":"woken","id":"gone","time":"05:00","date":"2026-10-26"}`, "", "past")
	if s := c.latest(); s != `{"alarms":[{"id":"once","time":"05:30","date":"2026-10-27","level":50,"next":"2026-10-26T23:30:00Z"},`+
		`{"id":"again","time":"09:15","date":"2026-11-02","level":60,"next":"2026-11-02T03:15:00Z"}],"timers":[],"reminders":[],"ringing":null,`+
		`"snoozed":[],"lastRing":null}` {
		t.Errorf("state %s", s)
	}
}

func TestWhatACommandCannotSayIsInvalid(t *testing.T) {
	c := newCapRig(t, at(26, 6, 0, 0))
	if err := c.apply(`{"timeZone":"Asia/Bishkek"}`); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{
		``, `[]`, `{}`, `{"id":"wake","time":"07:00"}`,
		`{"sessionId":"","id":"wake","time":"07:00"}`,
		`{"sessionId":null,"id":"wake","time":"07:00"}`,
		`{"sessionId":"woken","id":"Wake","time":"07:00"}`,
		`{"sessionId":"woken","id":"wake"}`,
		`{"sessionId":"woken","id":"wake","time":"7:00"}`,
		`{"sessionId":"woken","id":"wake","time":"24:00"}`,
		`{"sessionId":"woken","id":"wake","time":"07:00","days":[]}`,
		`{"sessionId":"woken","id":"wake","time":"07:00","days":["mon","mon"]}`,
		`{"sessionId":"woken","id":"wake","time":"07:00","days":["monday"]}`,
		`{"sessionId":"woken","id":"wake","time":"07:00","days":"daily"}`,
		`{"sessionId":"woken","id":"wake","time":"07:00","date":"2026-02-30"}`,
		`{"sessionId":"woken","id":"wake","time":"07:00","days":["mon"],"date":"2026-11-02"}`,
		`{"sessionId":"woken","id":"wake","time":"07:00","level":0}`,
		`{"sessionId":"woken","id":"wake","time":"07:00","level":101}`,
		`{"sessionId":"woken","id":"wake","time":"07:00","level":50.5}`,
		`{"sessionId":"woken","id":"wake","time":"07:00","label":"x"}`,
		`{"sessionId":"woken","id":"wake","time":"07:00"} {}`,
	} {
		c.want("set_alarm", args, "", "invalid")
	}
	for _, args := range []string{
		`{"sessionId":"woken"}`, `{"sessionId":"woken","id":"a","all":true}`, `{"sessionId":"woken","time":"07:00","id":"a"}`,
		`{"sessionId":"woken","all":false}`, `{"sessionId":"woken","time":"7:00"}`,
	} {
		c.want("cancel_alarm", args, "", "invalid")
	}
	for _, args := range []string{
		`{"sessionId":"woken","id":"tea"}`, `{"sessionId":"woken","id":"tea","seconds":0}`,
		`{"sessionId":"woken","id":"tea","seconds":86401}`, `{"sessionId":"woken","seconds":60}`,
	} {
		c.want("set_timer", args, "", "invalid")
	}
	for _, args := range []string{
		`{"sessionId":"woken","id":"a","seconds":60}`, `{"sessionId":"woken","seconds":60,"all":true}`,
		`{"sessionId":"woken","all":false}`, `{"sessionId":"woken","seconds":0}`,
	} {
		c.want("cancel_timer", args, "", "invalid")
	}
	if alarms, timers := c.book.List(); len(alarms)+len(timers) != 0 {
		t.Fatalf("something was set: %v %v", alarms, timers)
	}
}

// a cancel names what it took, or not_found; a time takes every alarm at it whatever its days, a length the timer set
// for it that ends first, and a timer cancel that names nothing takes the one that ends first
func TestCancelSemantics(t *testing.T) {
	c := newCapRig(t, at(26, 6, 0, 0))
	if err := c.apply(`{"timeZone":"Asia/Bishkek"}`); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{
		`{"sessionId":"woken","id":"a","time":"07:00","days":["mon"]}`,
		`{"sessionId":"woken","id":"b","time":"07:00","days":["tue"]}`,
		`{"sessionId":"woken","id":"c","time":"08:00","days":["mon"]}`,
		`{"sessionId":"woken","id":"d","time":"09:00","days":["mon"]}`,
	} {
		c.want("set_alarm", a, c.mustResult("set_alarm", a), "")
	}
	c.want("cancel_alarm", `{"sessionId":"woken","id":"x"}`, "", "not_found")
	c.want("cancel_alarm", `{"sessionId":"woken","time":"10:00"}`, "", "not_found")
	c.want("cancel_alarm", `{"sessionId":"woken","time":"07:00"}`, `{"cancelled":["a","b"]}`, "")
	c.want("cancel_alarm", `{"sessionId":"woken","id":"c"}`, `{"cancelled":["c"]}`, "")
	c.want("cancel_alarm", `{"sessionId":"woken","all":true}`, `{"cancelled":["d"]}`, "")
	c.want("cancel_alarm", `{"sessionId":"woken","all":true}`, "", "not_found")

	c.clock.set(at(26, 6, 0, 0))
	for _, tm := range []string{
		`{"sessionId":"woken","id":"long","seconds":600}`,
		`{"sessionId":"woken","id":"short","seconds":60}`,
		`{"sessionId":"woken","id":"long2","seconds":600}`,
		`{"sessionId":"woken","id":"mid","seconds":300}`,
	} {
		c.want("set_timer", tm, c.mustResult("set_timer", tm), "")
	}
	c.want("cancel_timer", `{"sessionId":"woken","seconds":120}`, "", "not_found")
	c.want("cancel_timer", `{"sessionId":"woken","seconds":600}`, `{"cancelled":["long"]}`, "")
	c.want("cancel_timer", `{"sessionId":"woken"}`, `{"cancelled":["short"]}`, "")
	c.want("cancel_timer", `{"sessionId":"woken","id":"mid"}`, `{"cancelled":["mid"]}`, "")
	c.want("cancel_timer", `{"sessionId":"woken","all":true}`, `{"cancelled":["long2"]}`, "")
	c.want("cancel_timer", `{"sessionId":"woken"}`, "", "not_found")
	c.want("cancel_timer", `{"sessionId":"woken","id":"x"}`, "", "not_found")
}

// mustResult is what a command's result must be: the set entry as the state shows it
func (c *capRig) mustResult(name, args string) string {
	c.t.Helper()
	var a struct {
		ID      string   `json:"id"`
		Time    string   `json:"time"`
		Days    []string `json:"days"`
		Seconds int      `json:"seconds"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		c.t.Fatal(err)
	}
	if name == "set_timer" {
		ends := c.clock.Now().Add(time.Duration(a.Seconds) * time.Second).UTC().Format(time.RFC3339)
		return `{"timer":{"id":"` + a.ID + `","seconds":` + jsonInt(a.Seconds) + `,"endsAt":"` + ends + `","next":"` + ends + `"}}`
	}
	days, _ := json.Marshal(a.Days)
	return `{"alarm":{"id":"` + a.ID + `","time":"` + a.Time + `","days":` + string(days) + `,"level":50},"replaced":false}`
}

func jsonInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestTheBooksRefusalsAreItsCodes(t *testing.T) {
	c := newCapRig(t, at(26, 6, 0, 0))
	if err := c.apply(`{"timeZone":"Asia/Bishkek"}`); err != nil {
		t.Fatal(err)
	}
	for i := range alarms.MaxAlarms {
		args := `{"sessionId":"woken","id":"a` + jsonInt(i) + `","time":"07:` + strings.Repeat("0", 2-len(jsonInt(i))) + jsonInt(i) + `"}`
		if _, code := c.command("set_alarm", args); code != "" {
			t.Fatalf("set %d: %s", i, code)
		}
	}
	c.want("set_alarm", `{"sessionId":"woken","id":"more","time":"08:00"}`, "", "full")
	for i := range alarms.MaxTimers {
		if _, code := c.command("set_timer", `{"sessionId":"woken","id":"t`+jsonInt(i)+`","seconds":60}`); code != "" {
			t.Fatalf("timer %d: %s", i, code)
		}
	}
	c.want("set_timer", `{"sessionId":"woken","id":"more","seconds":60}`, "", "full")

	// a book that cannot be saved takes nothing
	dir := filepath.Dir(c.path)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c.want("cancel_alarm", `{"sessionId":"woken","id":"a0"}`, `{"cancelled":["a0"]}`, "")
	c.want("set_alarm", `{"sessionId":"woken","id":"more","time":"08:00"}`, "", "store")
	c.want("cancel_timer", `{"sessionId":"woken","id":"t0"}`, `{"cancelled":["t0"]}`, "")
	c.want("set_timer", `{"sessionId":"woken","id":"more","seconds":60}`, "", "store")
}

// the state is what the book holds and the ring that sounds
func TestTheStateFollowsTheBookAndTheRing(t *testing.T) {
	c := newCapRig(t, at(26, 6, 59, 0))
	if s := c.latest(); s != `{"alarms":[],"timers":[],"reminders":[],"ringing":null,"snoozed":[],"lastRing":null}` {
		t.Fatalf("first state %s", s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.r.Run(ctx, alarms.Hooks{})
	}()
	t.Cleanup(func() { cancel(); <-done })

	await := func(want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			select {
			case s := <-c.p.State():
				if string(s) == want {
					return
				}
			case <-time.After(10 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				t.Fatalf("no state %s", want)
			}
		}
	}
	if _, code := c.command("set_timer", `{"sessionId":"woken","id":"tea","seconds":1}`); code != "" {
		t.Fatal(code)
	}
	await(`{"alarms":[],"timers":[{"id":"tea","seconds":1,"endsAt":"2026-10-26T00:59:01Z","next":"2026-10-26T00:59:01Z"}],` +
		`"reminders":[],"ringing":null,"snoozed":[],"lastRing":null}`)
	c.clock.set(at(26, 7, 0, 0))
	await(`{"alarms":[],"timers":[],"reminders":[],"ringing":{"kind":"timer","id":"tea"},"snoozed":[],"lastRing":null}`)
	c.r.Stop(alarms.StopButton)
	await(`{"alarms":[],"timers":[],"reminders":[],"ringing":null,"snoozed":[],` +
		`"lastRing":{"kind":"timer","id":"tea","at":"2026-10-26T01:00:00Z","stoppedBy":"button"}}`)
}

// a woken session of another device the server vouched for in this command sets an alarm and a timer here as a woken
// session of this device does; without the grant the same session is unknown and acts on nothing
func TestAVouchedSessionSetsAnAlarm(t *testing.T) {
	c := newCapRig(t, at(26, 6, 0, 0))
	if err := c.apply(`{"timeZone":"Asia/Bishkek"}`); err != nil {
		t.Fatal(err)
	}
	const alarm = `{"sessionId":"remote","id":"wake","time":"07:00","level":80}`
	if _, err := c.p.Command(context.Background(), "set_alarm", json.RawMessage(alarm)); err == nil {
		t.Fatal("set_alarm for an unknown session went")
	}
	vouched := capability.WithVouched(context.Background(), capability.Vouched{
		CallerDeviceID: "dev-a", SessionID: "remote", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	})
	if _, err := c.p.Command(vouched, "set_alarm", json.RawMessage(alarm)); err != nil {
		t.Fatalf("vouched set_alarm: %v", err)
	}
	if _, err := c.p.Command(vouched, "set_timer", json.RawMessage(`{"sessionId":"remote","id":"tea","seconds":60}`)); err != nil {
		t.Fatalf("vouched set_timer: %v", err)
	}
	if _, err := c.p.Command(vouched, "set_timer", json.RawMessage(`{"sessionId":"other","id":"tee","seconds":60}`)); err == nil {
		t.Fatal("a grant vouched for another session")
	}
	if alarms, _ := c.book.List(); len(alarms) != 1 {
		t.Fatalf("alarms = %v", alarms)
	}
}
