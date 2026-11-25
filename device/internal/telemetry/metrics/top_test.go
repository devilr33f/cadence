package metrics_test

import (
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
)

// a secret only a command line or an environment would hold: no output may ever carry it
const sentinel = "--api-key=sk_never_read_0123456789"

// guardFS fails the test on any path naming a command line or an environment, and runs onOpen before each open
type guardFS struct {
	t      *testing.T
	fsys   fstest.MapFS
	mu     sync.Mutex
	opened int
	onOpen func(name string)
}

func (g *guardFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, "cmdline") || strings.HasSuffix(name, "environ") {
		g.t.Errorf("opened %s", name)
	}
	g.mu.Lock()
	g.opened++
	onOpen := g.onOpen
	g.mu.Unlock()
	if onOpen != nil {
		onOpen(name)
	}
	return g.fsys.Open(name)
}

// proc is one process of a fake tree, its times in ticks of 1/100 s
type proc struct {
	pid               int
	comm              string
	utime, stime      uint64
	start, residentPg uint64
}

func (p proc) add(tree fstest.MapFS) {
	dir := fmt.Sprint(p.pid)
	tree[dir+"/stat"] = file(fmt.Sprintf("%d (%s) S 1 1 1 0 -1 4194560 100 0 0 0 %d %d 0 0 20 0 1 0 %d 1000000 %d "+
		"18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0\n", p.pid, p.comm, p.utime, p.stime, p.start, p.residentPg))
	tree[dir+"/statm"] = file(fmt.Sprintf("1000 %d 50 10 0 100 0\n", p.residentPg))
	tree[dir+"/cmdline"] = file("/bin/" + p.comm + "\x00" + sentinel + "\x00")
	tree[dir+"/environ"] = file("SECRET=" + sentinel + "\x00")
}

func procTree(ps ...proc) fstest.MapFS {
	tree := systemTree()
	for _, p := range ps {
		p.add(tree)
	}
	tree["self/stat"] = file("not a process")
	return tree
}

func guarded(t *testing.T, tree fstest.MapFS) *guardFS { return &guardFS{t: t, fsys: tree} }

func pids(ps []metrics.Proc) []int {
	out := make([]int, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.PID)
	}
	return out
}

func checkNoSentinel(t *testing.T, r metrics.Round) {
	t.Helper()
	if s := fmt.Sprint(r); strings.Contains(s, "sk_never_read") {
		t.Fatalf("a command line or environment reached the round: %s", s)
	}
}

// cpu is the share of one core: since the previous round (from zero for a process newer than it), or at the first
// round over each process's life; rss is the resident pages times the page size
func TestTopProcesses(t *testing.T) {
	// uptime 1000 s: a started 900 s in (age 100 s) with 20 s of cpu, b 500 s in with 10 s
	tree := procTree(
		proc{pid: 10, comm: "a", utime: 1500, stime: 500, start: 90000, residentPg: 100},
		proc{pid: 20, comm: "b", utime: 1000, stime: 0, start: 50000, residentPg: 300},
		// a kernel thread: no memory of its own
		proc{pid: 2, comm: "kthreadd", start: 0, residentPg: 0},
	)
	g := guarded(t, tree)
	c, _, clk := newCollector(metrics.Options{Proc: g, PageSize: 4096})
	r := c.Collect()
	checkNoSentinel(t, r)
	want := []metrics.Proc{{PID: 10, Comm: "a", CPU: 20, RSS: 100 * 4096}, {PID: 20, Comm: "b", CPU: 2, RSS: 300 * 4096},
		{PID: 2, Comm: "kthreadd", CPU: 0, RSS: 0}}
	if fmt.Sprint(r.Top.CPU) != fmt.Sprint(want) {
		t.Fatalf("top cpu %v, want %v", r.Top.CPU, want)
	}
	if got := pids(r.Top.RSS); fmt.Sprint(got) != "[20 10 2]" {
		t.Fatalf("top rss %v", got)
	}

	// 10 s later: a used 5 s of them, b 1 s, and c started 2 s ago and used 1 s, all of it within the 10 s
	clk.advance(10 * time.Second)
	tree["uptime"] = file("1010.00 1900.00\n")
	proc{pid: 10, comm: "a", utime: 1800, stime: 700, start: 90000, residentPg: 100}.add(tree)
	proc{pid: 20, comm: "b", utime: 1100, stime: 0, start: 50000, residentPg: 300}.add(tree)
	proc{pid: 30, comm: "c", utime: 100, stime: 0, start: 100800, residentPg: 50}.add(tree)
	r = c.Collect()
	// b and c tie: the larger process goes first
	want = []metrics.Proc{{PID: 10, Comm: "a", CPU: 50, RSS: 100 * 4096}, {PID: 20, Comm: "b", CPU: 10, RSS: 300 * 4096},
		{PID: 30, Comm: "c", CPU: 10, RSS: 50 * 4096}, {PID: 2, Comm: "kthreadd", CPU: 0, RSS: 0}}
	if fmt.Sprint(r.Top.CPU) != fmt.Sprint(want) {
		t.Fatalf("top cpu %v, want %v", r.Top.CPU, want)
	}
	if g.opened == 0 {
		t.Fatal("the guard saw no read")
	}
}

// a pid used again by a new process is a new process: its ticks are not taken against the old one's
func TestReusedPidIsANewProcess(t *testing.T) {
	tree := procTree(proc{pid: 10, comm: "old", utime: 50000, start: 1000, residentPg: 1})
	c, _, clk := newCollector(metrics.Options{Proc: guarded(t, tree)})
	c.Collect()
	clk.advance(10 * time.Second)
	tree["uptime"] = file("1010.00 1900.00\n")
	// 1 s of cpu since the round before: 10 %, where a delta from the old one's ticks would be none
	proc{pid: 10, comm: "new", utime: 100, start: 100500, residentPg: 1}.add(tree)
	r := c.Collect()
	if len(r.Top.CPU) != 1 || r.Top.CPU[0].Comm != "new" || r.Top.CPU[0].CPU != 10 {
		t.Fatalf("top cpu %v", r.Top.CPU)
	}
}

// each list holds the 10 largest
func TestTopTen(t *testing.T) {
	var ps []proc
	for i := 1; i <= 15; i++ {
		// cpu rises with the pid, memory falls with it
		ps = append(ps, proc{pid: 100 + i, comm: fmt.Sprint("p", i), utime: uint64(i) * 100, start: 0, residentPg: uint64(100 - i)})
	}
	c, _, _ := newCollector(metrics.Options{Proc: guarded(t, procTree(ps...))})
	r := c.Collect()
	if got := fmt.Sprint(pids(r.Top.CPU)); got != "[115 114 113 112 111 110 109 108 107 106]" {
		t.Fatalf("top cpu %s", got)
	}
	if got := fmt.Sprint(pids(r.Top.RSS)); got != "[101 102 103 104 105 106 107 108 109 110]" {
		t.Fatalf("top rss %s", got)
	}
}

// comm is what lies between the outer parentheses, made printable utf-8 of at most 32 bytes
func TestCommSanitized(t *testing.T) {
	for _, w := range []struct{ comm, want string }{
		{"my (weird) name", "my (weird) name"},
		{"x) S 1 2 (y", "x) S 1 2 (y"},
		{"tab\there\x01", "tab?here?"},
		{"\xff\xfeok", "��ok"},
		{strings.Repeat("abcdefghij", 4), strings.Repeat("abcdefghij", 3) + "ab"},
		{strings.Repeat("\xff", 15), strings.Repeat("�", 10)},
		{"", ""},
	} {
		c, _, _ := newCollector(metrics.Options{Proc: guarded(t, procTree(proc{pid: 7, comm: w.comm, residentPg: 1}))})
		r := c.Collect()
		if len(r.Top.RSS) != 1 {
			t.Fatalf("%q: top rss %v", w.comm, r.Top.RSS)
		}
		got := r.Top.RSS[0].Comm
		if got != w.want || !utf8.ValidString(got) || len(got) > 32 {
			t.Errorf("comm %q = %q, want %q", w.comm, got, w.want)
		}
	}
}

// a process that exits between its two reads, or before the first, is left out; so is one whose stat is garbled
func TestVanishedProcessesSkipped(t *testing.T) {
	tree := procTree(
		proc{pid: 10, comm: "stays", residentPg: 5},
		proc{pid: 20, comm: "exits", residentPg: 6},
		proc{pid: 40, comm: "midread", residentPg: 7},
	)
	// exited before its stat was read: the directory still listed
	delete(tree, "20/stat")
	delete(tree, "20/statm")
	tree["50/stat"] = file("50 no parentheses here\n")
	tree["50/statm"] = file("1 1 1 1 1 1 1\n")
	tree["60/stat"] = file("60 (short) S 1\n")
	tree["60/statm"] = file("1 1 1 1 1 1 1\n")
	g := guarded(t, tree)
	g.onOpen = func(name string) {
		if name == "40/stat" {
			delete(tree, "40/statm")
		}
	}
	c, app, _ := newCollector(metrics.Options{Proc: g})
	r := c.Collect()
	if got := fmt.Sprint(pids(r.Top.RSS)); got != "[10]" {
		t.Fatalf("top rss %s", got)
	}
	// the garbled ones count, once for the round
	if n := count(t, metrics.Round{Points: app.Points()}, "cadence.metrics_errors"); n != 1 {
		t.Fatalf("errors %d", n)
	}
}

// with no uptime the first round has no cpu: its processes are left out of the cpu list alone; the rounds after it need
// none
func TestNoUptime(t *testing.T) {
	tree := procTree(proc{pid: 10, comm: "a", utime: 100, residentPg: 1})
	delete(tree, "uptime")
	c, app, clk := newCollector(metrics.Options{Proc: guarded(t, tree)})
	r := c.Collect()
	if len(r.Top.CPU) != 0 || len(r.Top.RSS) != 1 {
		t.Fatalf("top %v", r.Top)
	}
	clk.advance(10 * time.Second)
	proc{pid: 10, comm: "a", utime: 200, residentPg: 1}.add(tree)
	proc{pid: 20, comm: "b", utime: 50, start: 5000, residentPg: 1}.add(tree)
	r = c.Collect()
	want := []metrics.Proc{{PID: 10, Comm: "a", CPU: 10, RSS: 4096}, {PID: 20, Comm: "b", CPU: 5, RSS: 4096}}
	if fmt.Sprint(r.Top.CPU) != fmt.Sprint(want) {
		t.Fatalf("top cpu %v", r.Top.CPU)
	}
	// the uptime was missed once, at the first round
	if n := count(t, metrics.Round{Points: app.Points()}, "cadence.metrics_errors"); n != 1 {
		t.Fatalf("errors %d", n)
	}
}

// a child a few ms old with a tick is no busy process: after the first round its tick counts over the interval, and at
// the first round its life counts as a second at least
func TestYoungProcessesNotInflated(t *testing.T) {
	// uptime 1000 s: the child started 10 ms ago with a tick, which over its life alone would be 100 %
	tree := procTree(proc{pid: 10, comm: "daemon", residentPg: 1}, proc{pid: 20, comm: "child", utime: 1, start: 99999, residentPg: 1})
	c, _, clk := newCollector(metrics.Options{Proc: guarded(t, tree)})
	r := c.Collect()
	want := []metrics.Proc{{PID: 20, Comm: "child", CPU: 1, RSS: 4096}, {PID: 10, Comm: "daemon", CPU: 0, RSS: 4096}}
	if fmt.Sprint(r.Top.CPU) != fmt.Sprint(want) {
		t.Fatalf("first round: top cpu %v", r.Top.CPU)
	}

	// 10 s later the daemon used 3 s, and another child 15 ms old used a tick
	clk.advance(10 * time.Second)
	tree["uptime"] = file("1010.00 1900.00\n")
	proc{pid: 10, comm: "daemon", utime: 300, residentPg: 1}.add(tree)
	proc{pid: 30, comm: "child2", utime: 1, start: 100998, residentPg: 1}.add(tree)
	r = c.Collect()
	want = []metrics.Proc{{PID: 10, Comm: "daemon", CPU: 30, RSS: 4096}, {PID: 30, Comm: "child2", CPU: 0.1, RSS: 4096},
		{PID: 20, Comm: "child", CPU: 0, RSS: 4096}}
	if fmt.Sprint(r.Top.CPU) != fmt.Sprint(want) {
		t.Fatalf("later round: top cpu %v", r.Top.CPU)
	}
}
