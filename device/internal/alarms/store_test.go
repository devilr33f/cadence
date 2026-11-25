package alarms_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTheStoreKeepsTheBookInAFileOnlyItsOwnerReads(t *testing.T) {
	f := newFixture(t)
	now := at(26, 6, 0, 0)
	b := f.open(now)
	f.mustSetAlarm(b, now, alarm("wk", 7, 0, weekdays))
	quiet := dated("once", 23, 59, date(2026, 11, 3))
	quiet.Level = 1
	f.mustSetAlarm(b, now, quiet)
	f.mustSetTimer(b, now, "tea", 300)

	raw, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"alarms":[{"id":"wk","time":"07:00","days":["mon","tue","wed","thu","fri"],"level":50,`+
		`"lastRungAt":%d},{"id":"once","time":"23:59","date":"2026-11-03","level":1}],`+
		`"timers":[{"id":"tea","seconds":300,"endsAt":%d}]}`+"\n", at(23, 7, 0, 0).Unix(), now.Unix()+300)
	if string(raw) != want {
		t.Errorf("file =\n%s\nwant\n%s", raw, want)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(f.path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, %v", fi.Mode(), err)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(f.path))
	if len(entries) != 1 {
		t.Errorf("state dir holds %d entries, want only the book", len(entries))
	}

	alarmsBack, timersBack := f.stored()
	before, timersBefore := b.List()
	if fmt.Sprint(alarmsBack) != fmt.Sprint(before) || fmt.Sprint(timersBack) != fmt.Sprint(timersBefore) {
		t.Errorf("reloaded %v %v, want %v %v", alarmsBack, timersBack, before, timersBefore)
	}
	if len(f.logs.events("alarms_store_invalid")) != 0 {
		t.Error("a file the book wrote read as invalid")
	}
}

func TestAMissingFileIsAnEmptyBookQuietly(t *testing.T) {
	f := newFixture(t)
	b := f.open(at(26, 6, 0, 0))
	wantIDs(t, "alarms", alarmIDs(b))
	wantIDs(t, "timers", timerIDs(b))
	if len(f.logs.lines()) != 0 {
		t.Errorf("logged %v", f.logs.lines())
	}
}

func TestAFileThatCannotBeTrustedIsAnEmptyBookAndOneLogLineWithoutItsContent(t *testing.T) {
	const ok = `{"id":"secretid","time":"07:00","days":["mon"],"level":50}`
	book := func(alarm, timer string) string {
		return `{"alarms":[` + alarm + `],"timers":[` + timer + `]}`
	}
	entry := func(fields string) string { return `{"id":"secretid",` + fields + `}` }
	for _, text := range []string{
		``,
		`not json at all`,
		`[]`,
		`null`,
		`{"alarms":[],"timers":[]} trailing`,
		`{"alarms":[],"timers":[],"extra":1}`,
		`{"Alarms":[],"timers":[]}`,
		`{"alarms":null,"timers":[]}`,
		`{"alarms":[],"alarms":[],"timers":[]}`,
		book(entry(`"time":"07:00","days":["mon"],"level":50,"level":60`), ""),
		book(`{"id":"secretid","id":"other","time":"07:00","days":["mon"],"level":50}`, ""),
		strings.Repeat(" ", 70000) + book("", ""),
		book(ok+","+ok, ""),
		book(entry(`"time":"07:00","days":["mon","mon"],"level":50`), ""),
		book(entry(`"time":"07:00","days":["funday"],"level":50`), ""),
		book(entry(`"time":"07:00","days":[],"level":50`), ""),
		book(entry(`"time":"07:00","level":50`), ""),
		book(entry(`"time":"07:00","days":["mon"],"date":"2026-11-03","level":50`), ""),
		book(entry(`"time":"07:00","date":"2026-02-30","level":50`), ""),
		book(entry(`"time":"07:00","date":null,"level":50`), ""),
		book(entry(`"time":"24:00","days":["mon"],"level":50`), ""),
		book(entry(`"time":"7:00","days":["mon"],"level":50`), ""),
		book(entry(`"time":"07:60","days":["mon"],"level":50`), ""),
		book(entry(`"time":"07:00","days":["mon"],"level":0`), ""),
		book(entry(`"time":"07:00","days":["mon"],"level":101`), ""),
		book(entry(`"time":"07:00","days":["mon"],"level":7.5`), ""),
		book(entry(`"time":"07:00","days":["mon"],"level":"50"`), ""),
		book(entry(`"time":"07:00","days":["mon"]`), ""),
		book(entry(`"time":"07:00","days":["mon"],"level":50,"lastRungAt":1.5`), ""),
		book(entry(`"time":"07:00","days":["mon"],"level":50,"color":"red"`), ""),
		book(entry(`"Time":"07:00","days":["mon"],"level":50`), ""),
		book(`{"id":"Secretid","time":"07:00","days":["mon"],"level":50}`, ""),
		book(`{"id":"secretid12345678x","time":"07:00","days":["mon"],"level":50}`, ""),
		book("", entry(`"seconds":0,"endsAt":5`)),
		book("", entry(`"seconds":86401,"endsAt":5`)),
		book("", entry(`"seconds":60`)),
		book("", entry(`"seconds":60,"endsAt":"5"`)),
		book("", entry(`"seconds":60,"endsAt":9223372036854775808`)),
		book("", entry(`"seconds":60,"endsAt":5`)+","+entry(`"seconds":90,"endsAt":5`)),
	} {
		f := newFixture(t)
		if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		b := f.open(at(26, 6, 0, 0))
		label := text
		if len(label) > 80 {
			label = label[len(label)-80:]
		}
		if len(alarmIDs(b)) != 0 || len(timerIDs(b)) != 0 {
			t.Errorf("%s: loaded %v %v", label, alarmIDs(b), timerIDs(b))
		}
		if n := len(f.logs.lines()); n != 1 || len(f.logs.events("alarms_store_invalid")) != 1 {
			t.Errorf("%s: logged %v", label, f.logs.lines())
		}
		if f.logs.contains("secretid") {
			t.Errorf("%s: the log quotes the file", label)
		}
	}
}

func TestTheLimitsHoldOnDisk(t *testing.T) {
	file := func(nAlarms, nTimers int) string {
		var as, ts []string
		for i := range nAlarms {
			as = append(as, fmt.Sprintf(`{"id":"a%d","time":"07:%02d","days":["mon"],"level":50}`, i, i))
		}
		for i := range nTimers {
			ts = append(ts, fmt.Sprintf(`{"id":"t%d","seconds":60,"endsAt":%d}`, i, at(26, 6, 1, 0).Unix()))
		}
		return `{"alarms":[` + strings.Join(as, ",") + `],"timers":[` + strings.Join(ts, ",") + `]}`
	}
	for _, c := range []struct {
		alarms, timers int
		loads          bool
	}{{10, 5, true}, {11, 0, false}, {0, 6, false}} {
		f := newFixture(t)
		if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.path, []byte(file(c.alarms, c.timers)), 0o600); err != nil {
			t.Fatal(err)
		}
		b := f.open(at(26, 6, 0, 0))
		gotA, gotT := len(alarmIDs(b)), len(timerIDs(b))
		if c.loads && (gotA != c.alarms || gotT != c.timers || len(f.logs.lines()) != 0) {
			t.Errorf("%d/%d: loaded %d/%d, logs %v", c.alarms, c.timers, gotA, gotT, f.logs.lines())
		}
		if !c.loads && (gotA != 0 || gotT != 0 || len(f.logs.events("alarms_store_invalid")) != 1) {
			t.Errorf("%d/%d over the limit: loaded %d/%d", c.alarms, c.timers, gotA, gotT)
		}
	}
}
