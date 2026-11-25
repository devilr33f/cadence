package metrics_test

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// clock is a test's time, moved by hand
type clock struct{ at time.Time }

func (c *clock) now() time.Time          { return c.at }
func (c *clock) advance(d time.Duration) { c.at = c.at.Add(d) }

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

// a tree with two cores and no processes
func systemTree() fstest.MapFS {
	return fstest.MapFS{
		"stat": file("cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 50 0 50 350 50 0 0 0 0 0\ncpu1 50 0 50 350 50 0 0 0 0 0\n" +
			"intr 12345 1 2 3\nctxt 999\n"),
		"loadavg": file("0.50 1.25 2.00 1/123 4567\n"),
		"meminfo": file("MemTotal:        1000 kB\nMemFree:          100 kB\nMemAvailable:     400 kB\nBuffers: 1 kB\n"),
		"uptime":  file("1000.00 1800.00\n"),
	}
}

func newCollector(o metrics.Options) (*metrics.Collector, *metrics.App, *clock) {
	c := &clock{at: t0}
	if o.App == nil {
		o.App = metrics.NewApp(t0)
	}
	o.Now = c.now
	if o.PageSize == 0 {
		o.PageSize = 4096
	}
	return metrics.NewCollector(o), o.App, c
}

// value is the point of name with exactly attrs, if the round has one
func value(r metrics.Round, name string, attrs ...metrics.Attr) (metrics.Point, bool) {
	for _, p := range r.Points {
		if p.Name == name && slices.Equal(p.Attrs, attrs) {
			return p, true
		}
	}
	return metrics.Point{}, false
}

func gauge(t *testing.T, r metrics.Round, name string, attrs ...metrics.Attr) float64 {
	t.Helper()
	p, ok := value(r, name, attrs...)
	if !ok {
		t.Fatalf("no %s %v in %v", name, attrs, r.Points)
	}
	if p.Kind != metrics.KindGauge {
		t.Fatalf("%s is no gauge", name)
	}
	return p.Value
}

func count(t *testing.T, r metrics.Round, name string, attrs ...metrics.Attr) uint64 {
	t.Helper()
	p, ok := value(r, name, attrs...)
	if !ok {
		t.Fatalf("no %s %v in %v", name, attrs, r.Points)
	}
	if p.Kind != metrics.KindSum {
		t.Fatalf("%s is no sum", name)
	}
	return p.Count
}

func absent(t *testing.T, r metrics.Round, name string) {
	t.Helper()
	for _, p := range r.Points {
		if p.Name == name {
			t.Fatalf("%s reported: %v", name, p)
		}
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// utilization is the busy share of the ticks between two rounds, total and per core; idle and iowait are idle
func TestCPUUtilization(t *testing.T) {
	tree := systemTree()
	c, _, clk := newCollector(metrics.Options{Proc: tree})
	r := c.Collect()
	// the first round has nothing to take a delta from
	absent(t, r, "system.cpu.utilization")

	// total: busy +200 of +500; cpu0: busy +150 of +200; cpu1: busy +50 of +300
	tree["stat"] = file("cpu  250 10 140 950 150 0 0 0 5 5\ncpu0 200 0 50 400 50 0 0 0 0 0\ncpu1 100 0 50 550 100 0 0 0 0 0\n")
	clk.advance(30 * time.Second)
	r = c.Collect()
	for _, w := range []struct {
		core string
		want float64
	}{{"", 200.0 / 500}, {"0", 150.0 / 200}, {"1", 50.0 / 300}} {
		var attrs []metrics.Attr
		if w.core != "" {
			attrs = []metrics.Attr{{Key: "cpu", Value: w.core}}
		}
		if got := gauge(t, r, "system.cpu.utilization", attrs...); !near(got, w.want) {
			t.Errorf("cpu %q = %v, want %v", w.core, got, w.want)
		}
	}

	if n := count(t, r, "cadence.metrics_errors"); n != 0 {
		t.Fatalf("errors %d", n)
	}

	// no tick passed: no value, never a division by zero, and no error
	r = c.Collect()
	absent(t, r, "system.cpu.utilization")
	if n := count(t, r, "cadence.metrics_errors"); n != 0 {
		t.Fatalf("errors %d with no tick passed", n)
	}

	// counters that went back (a core reset): no value for it, and one error for the round
	tree["stat"] = file("cpu  250 10 140 950 150 0 0 0 5 5\ncpu0 10 0 10 10 0 0 0 0 0 0\ncpu1 10 0 10 10 0 0 0 0 0 0\n")
	r = c.Collect()
	absent(t, r, "system.cpu.utilization")
	if n := count(t, r, "cadence.metrics_errors"); n != 1 {
		t.Fatalf("errors %d, want 1", n)
	}
}

func TestLoadAndMemory(t *testing.T) {
	c, _, _ := newCollector(metrics.Options{Proc: systemTree()})
	r := c.Collect()
	for name, want := range map[string]float64{
		"system.cpu.load_average.1m": 0.5, "system.cpu.load_average.5m": 1.25, "system.cpu.load_average.15m": 2,
		"system.memory.limit": 1000 * 1024, "system.memory.available": 400 * 1024, "system.memory.used": 600 * 1024,
	} {
		if got := gauge(t, r, name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if n := count(t, r, "cadence.metrics_errors"); n != 0 {
		t.Fatalf("errors %d", n)
	}
}

// statfs per board mount, through the func the board's collector is given; a mount it cannot read is left out
func TestFilesystems(t *testing.T) {
	statfs := func(path string) (total, free uint64, err error) {
		switch path {
		case "/data":
			return 4 << 30, 1 << 30, nil
		case "/tmp":
			return 64 << 20, 60 << 20, nil
		}
		return 0, 0, errors.New("no such mount")
	}
	c, _, _ := newCollector(metrics.Options{Proc: systemTree(), Mounts: []string{"/data", "/tmp", "/gone"}, Statfs: statfs})
	r := c.Collect()
	for _, w := range []struct {
		mount       string
		total, free float64
	}{{"/data", 4 << 30, 1 << 30}, {"/tmp", 64 << 20, 60 << 20}} {
		at := metrics.Attr{Key: "mountpoint", Value: w.mount}
		if got := gauge(t, r, "system.filesystem.limit", at); got != w.total {
			t.Errorf("%s total %v", w.mount, got)
		}
		if got := gauge(t, r, "system.filesystem.free", at); got != w.free {
			t.Errorf("%s free %v", w.mount, got)
		}
	}
	if _, ok := value(r, "system.filesystem.limit", metrics.Attr{Key: "mountpoint", Value: "/gone"}); ok {
		t.Fatal("a mount statfs refused is reported")
	}
	if n := count(t, r, "cadence.metrics_errors"); n != 1 {
		t.Fatalf("errors %d, want 1", n)
	}
}

// the sensor reads milli-degrees; none declared is no metric and no error, one unreadable or garbled is an error
func TestTemperature(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, w := range []struct {
		name   string
		sensor string
		want   float64
		ok     bool
		errors uint64
	}{
		{"present", write("temp", "45500\n"), 45.5, true, 0},
		{"below zero", write("cold", "-1250"), -1.25, true, 0},
		{"none declared", "", 0, false, 0},
		{"unreadable", filepath.Join(dir, "missing"), 0, false, 1},
		{"garbage", write("bad", "hot\n"), 0, false, 1},
		{"empty", write("empty", ""), 0, false, 1},
	} {
		t.Run(w.name, func(t *testing.T) {
			c, _, _ := newCollector(metrics.Options{Proc: systemTree(), TempSensor: w.sensor})
			r := c.Collect()
			p, ok := value(r, "system.temperature")
			if ok != w.ok || ok && !near(p.Value, w.want) {
				t.Fatalf("temperature %v %v, want %v %v", p.Value, ok, w.want, w.ok)
			}
			if n := count(t, r, "cadence.metrics_errors"); n != w.errors {
				t.Fatalf("errors %d, want %d", n, w.errors)
			}
		})
	}
}

// a missing or malformed source skips its metrics for the round and counts once; nothing panics
func TestBrokenSourcesSkipped(t *testing.T) {
	for _, w := range []struct {
		name    string
		file    string
		content *string
		gone    []string
	}{
		{"no stat", "stat", nil, nil},
		{"stat without cpu", "stat", ptr("intr 1 2\n"), nil},
		{"stat short", "stat", ptr("cpu  1 2\n"), nil},
		{"stat garbage", "stat", ptr("cpu  a b c d\n"), nil},
		{"no loadavg", "loadavg", nil, []string{"system.cpu.load_average.1m"}},
		{"loadavg short", "loadavg", ptr("0.1 0.2\n"), []string{"system.cpu.load_average.1m"}},
		{"loadavg garbage", "loadavg", ptr("x y z 1/2 3\n"), []string{"system.cpu.load_average.15m"}},
		{"loadavg negative", "loadavg", ptr("-1 0 0 1/2 3\n"), []string{"system.cpu.load_average.5m"}},
		{"no meminfo", "meminfo", nil, []string{"system.memory.limit"}},
		{"meminfo without available", "meminfo", ptr("MemTotal: 1000 kB\nMemFree: 10 kB\n"), []string{"system.memory.used"}},
		{"meminfo garbage", "meminfo", ptr("MemTotal: lots kB\nMemAvailable: 1 kB\n"), []string{"system.memory.limit"}},
		{"available over total", "meminfo", ptr("MemTotal: 10 kB\nMemAvailable: 20 kB\n"), []string{"system.memory.available"}},
		{"empty", "meminfo", ptr(""), []string{"system.memory.limit"}},
	} {
		t.Run(w.name, func(t *testing.T) {
			tree := systemTree()
			c, _, clk := newCollector(metrics.Options{Proc: tree})
			c.Collect()
			if w.content == nil {
				delete(tree, w.file)
			} else {
				tree[w.file] = file(*w.content)
			}
			// cpu needs a second round to report
			if f := withStat(tree["stat"]); f != nil {
				tree["stat"] = f
			}
			clk.advance(10 * time.Second)
			r := c.Collect()
			if w.file == "stat" {
				absent(t, r, "system.cpu.utilization")
			}
			for _, name := range w.gone {
				absent(t, r, name)
			}
			if n := count(t, r, "cadence.metrics_errors"); n != 1 {
				t.Fatalf("errors %d, want 1", n)
			}
			// the rest still reports
			if w.file != "meminfo" {
				gauge(t, r, "system.memory.limit")
			} else {
				gauge(t, r, "system.cpu.load_average.1m")
			}
		})
	}
}

func ptr(s string) *string { return &s }

// withStat moves a healthy stat on, and leaves a broken or missing one as it is
func withStat(f *fstest.MapFile) *fstest.MapFile {
	if f == nil || string(f.Data) != string(systemTree()["stat"].Data) {
		return f
	}
	return file("cpu  200 0 200 1400 200 0 0 0 0 0\ncpu0 100 0 100 700 100 0 0 0 0 0\ncpu1 100 0 100 700 100 0 0 0 0 0\n")
}

// a component's own series (the screen's player) join the round as it gives them, before cadenced's counts
func TestExtraSeriesJoinTheRound(t *testing.T) {
	extra := []metrics.Point{
		{Name: "cadence.player.buffered", Kind: metrics.KindGauge, Value: 12.5},
		{Name: "cadence.player.frame_drops", Kind: metrics.KindSum, Count: 3},
	}
	c, _, _ := newCollector(metrics.Options{Proc: systemTree(), Extra: func() []metrics.Point { return extra }})
	r := c.Collect()
	if v := gauge(t, r, "cadence.player.buffered"); !near(v, 12.5) {
		t.Fatalf("buffered %v", v)
	}
	if n := count(t, r, "cadence.player.frame_drops"); n != 3 {
		t.Fatalf("drops %d", n)
	}
	extra = nil
	absent(t, c.Collect(), "cadence.player.buffered")
}
