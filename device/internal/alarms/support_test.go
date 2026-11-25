package alarms_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	// the zones the tests name, whatever the host has
	_ "time/tzdata"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
)

// Asia/Bishkek is +06:00 all year; 2026-10-26 is a Monday
var bishkek = mustZone("Asia/Bishkek")

const (
	weekdays alarms.Days = 0x1F
	mondays  alarms.Days = 0x01
)

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// at is a moment of October 2026 in Bishkek; day 32 is November 1st
func at(d, hour, minute, second int) time.Time {
	return time.Date(2026, time.October, d, hour, minute, second, 0, bishkek)
}

func alarm(id string, hour, minute int, days alarms.Days) alarms.Alarm {
	return alarms.Alarm{ID: id, Hour: hour, Minute: minute, Days: days, Level: 50}
}

func dated(id string, hour, minute int, on alarms.Date) alarms.Alarm {
	return alarms.Alarm{ID: id, Hour: hour, Minute: minute, Date: on, Level: 50}
}

func date(year, month, d int) alarms.Date {
	return alarms.Date{Year: year, Month: time.Month(month), Day: d}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) lines() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for line := range strings.Lines(l.b.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			panic(err)
		}
		out = append(out, m)
	}
	return out
}

// events are the lines named msg, each as its attributes
func (l *logBuf) events(msg string) []map[string]any {
	var out []map[string]any
	for _, m := range l.lines() {
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func (l *logBuf) contains(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Contains(l.b.String(), s)
}

// rule 8: what the book logs is ids, kinds, codes and numbers
var loggedKeys = []string{"time", "level", "msg", "id", "kind", "reason", "seconds", "code", "minutes", "auto", "auto_snooze", "sound"}

type fixture struct {
	t     *testing.T
	path  string
	clock *fakeClock
	logs  *logBuf
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, path: alarms.StorePath(t.TempDir()), clock: &fakeClock{}, logs: &logBuf{}}
	t.Cleanup(func() {
		for _, m := range f.logs.lines() {
			for k := range m {
				if !slices.Contains(loggedKeys, k) {
					t.Errorf("log %v carries %q", m["msg"], k)
				}
			}
		}
	})
	return f
}

// open is the device starting at now: a new book over whatever the file holds, in Bishkek
func (f *fixture) open(now time.Time) *alarms.Book {
	f.t.Helper()
	return f.openIn(now, "Asia/Bishkek")
}

func (f *fixture) openIn(now time.Time, zone string) *alarms.Book {
	f.t.Helper()
	b := f.openUnzoned(now)
	if err := b.SetZone(zone); err != nil {
		f.t.Fatal(err)
	}
	return b
}

// openUnzoned is a start before the capability config sets the zone
func (f *fixture) openUnzoned(now time.Time) *alarms.Book {
	f.t.Helper()
	f.clock.set(now)
	b, err := alarms.NewBook(f.path, f.clock, slog.New(slog.NewJSONHandler(f.logs, nil)))
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (f *fixture) setAlarm(b *alarms.Book, now time.Time, a alarms.Alarm) error {
	f.clock.set(now)
	_, _, err := b.SetAlarm(a)
	return err
}

func (f *fixture) mustSetAlarm(b *alarms.Book, now time.Time, a alarms.Alarm) {
	f.t.Helper()
	if err := f.setAlarm(b, now, a); err != nil {
		f.t.Fatalf("set %s: %v", a.ID, err)
	}
}

func (f *fixture) setTimer(b *alarms.Book, now time.Time, id string, seconds int) error {
	f.clock.set(now)
	_, err := b.SetTimer(id, seconds)
	return err
}

func (f *fixture) mustSetTimer(b *alarms.Book, now time.Time, id string, seconds int) {
	f.t.Helper()
	if err := f.setTimer(b, now, id, seconds); err != nil {
		f.t.Fatalf("set timer %s: %v", id, err)
	}
}

// stored is what a fresh book reads from the file, quietly
func (f *fixture) stored() ([]alarms.Alarm, []alarms.Timer) {
	f.t.Helper()
	b, err := alarms.NewBook(f.path, f.clock, slog.New(slog.DiscardHandler))
	if err != nil {
		f.t.Fatal(err)
	}
	return b.List()
}

func alarmIDs(b *alarms.Book) []string {
	list, _ := b.List()
	return ids(list, func(a alarms.Alarm) string { return a.ID })
}

func timerIDs(b *alarms.Book) []string {
	_, list := b.List()
	return ids(list, func(t alarms.Timer) string { return t.ID })
}

func ids[T any](list []T, id func(T) string) []string {
	out := []string{}
	for _, e := range list {
		out = append(out, id(e))
	}
	return out
}

func ringIDs(rings []alarms.Ring) []string {
	return ids(rings, func(r alarms.Ring) string { return r.ID })
}

func wantIDs(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// changed is whether a change came on a subscriber's channel since the last look
func changed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
