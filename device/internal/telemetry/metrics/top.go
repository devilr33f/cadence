package metrics

import (
	"cmp"
	"io/fs"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// linux reports process times in USER_HZ, 100 on every architecture a board runs
	userHZ = 100
	// the most processes a round looks at; beyond them the round counts an error
	maxProcs = 4096
	// each top list's length
	topN = 10
	// a process's name as reported, at most
	maxComm = 32
	// a pid's stat line is some hundred bytes
	maxPidFile = 4 << 10
	// the shortest life a first round averages a process's cpu over: a child a few ms old with one tick is no busy one
	minAge = 1.0
)

// Proc is one process of a top list: its name as the kernel keeps it (comm), never its command line; cpu is the
// percent of one core it used since the round before (from zero for one newer than that round), or at the first round
// its average since it started (over a second at least); rss its resident bytes.
type Proc struct {
	PID  int     `json:"pid"`
	Comm string  `json:"comm"`
	CPU  float64 `json:"cpu"`
	RSS  uint64  `json:"rss"`
}

// Top are the 10 processes using the most cpu and the 10 holding the most memory; neither is nil.
type Top struct {
	CPU []Proc `json:"topCpu"`
	RSS []Proc `json:"topRss"`
}

// a pid used again is another process: it started at another tick
type procKey struct {
	pid   int
	start uint64
}

// procs samples /proc/<pid>/stat and statm, and nothing else of a process
type procs struct {
	fsys     fs.FS
	pageSize uint64
	// each process's ticks at the round before; nil before the first round
	prev   map[procKey]uint64
	prevAt time.Time
}

// first is whether no round came before: cpu is then each process's average over its life, which needs the uptime
func (p *procs) first() bool { return p.prev == nil }

type sample struct {
	Proc
	key   procKey
	ticks uint64
	// cpu is known: a delta since the round before, or at the first round the process's life with the uptime known
	known bool
}

// sample reads every process at now; uptime (haveUptime) counts at the first round alone. malformed reports whether
// any process's files were garbled or the processes could not all be read. A process gone before its files are read
// is left out.
func (p *procs) sample(now time.Time, uptime float64, haveUptime bool) (Top, bool) {
	entries, err := fs.ReadDir(p.fsys, ".")
	if err != nil {
		return emptyTop(), true
	}
	first, malformed := p.first(), false
	cur := make(map[procKey]uint64)
	var all []sample
	elapsed := now.Sub(p.prevAt).Seconds()
	for _, e := range entries {
		pid, ok := pidOf(e)
		if !ok {
			continue
		}
		if len(all) >= maxProcs {
			malformed = true
			break
		}
		s, ok, bad := p.read(pid)
		malformed = malformed || bad
		if !ok {
			continue
		}
		cur[s.key] = s.ticks
		prev, seen := p.prev[s.key]
		switch {
		case seen && elapsed > 0:
			s.CPU, s.known = percent(s.ticks-min(prev, s.ticks), elapsed), true
		case !first && elapsed > 0:
			// newer than the round before: all it used, it used since then
			s.CPU, s.known = percent(s.ticks, elapsed), true
		case first && haveUptime:
			s.CPU, s.known = percent(s.ticks, max(uptime-float64(s.key.start)/userHZ, minAge)), true
		}
		all = append(all, s)
	}
	p.prev, p.prevAt = cur, now
	return top(all), malformed
}

func pidOf(e fs.DirEntry) (int, bool) {
	name := e.Name()
	if !e.IsDir() || name == "" || strings.TrimLeft(name, "0123456789") != "" {
		return 0, false
	}
	pid, err := strconv.ParseInt(name, 10, 32)
	if err != nil || pid < 1 {
		return 0, false
	}
	return int(pid), true
}

// read takes one process's stat and statm; ok is false when it is gone or garbled, and bad when garbled
func (p *procs) read(pid int) (s sample, ok, bad bool) {
	dir := strconv.Itoa(pid) + "/"
	b, err := readCapped(p.fsys, dir+"stat", maxPidFile)
	if err != nil {
		return sample{}, false, false
	}
	comm, ticks, start, ok := parseStat(string(b))
	if !ok {
		return sample{}, false, true
	}
	b, err = readCapped(p.fsys, dir+"statm", maxPidFile)
	if err != nil {
		return sample{}, false, false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return sample{}, false, true
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return sample{}, false, true
	}
	return sample{
		Proc: Proc{PID: pid, Comm: sanitizeComm(comm), RSS: pages * p.pageSize},
		key:  procKey{pid: pid, start: start}, ticks: ticks,
	}, true, false
}

// parseStat takes the name between the outer parentheses, which may hold anything, then utime and stime (fields 14
// and 15) and starttime (22)
func parseStat(s string) (comm string, ticks, start uint64, ok bool) {
	open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return "", 0, 0, false
	}
	rest := strings.Fields(s[end+1:])
	if len(rest) < 20 {
		return "", 0, 0, false
	}
	var v [3]uint64
	for i, f := range []string{rest[11], rest[12], rest[19]} {
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return "", 0, 0, false
		}
		v[i] = n
	}
	return s[open+1 : end], v[0] + v[1], v[2], true
}

// percent of one core: ticks used over seconds, to a tenth
func percent(ticks uint64, seconds float64) float64 {
	return math.Round(float64(ticks)/userHZ/seconds*1000) / 10
}

// sanitizeComm keeps printable utf-8, each invalid byte as U+FFFD and anything else unprintable as '?', at most 32 bytes
func sanitizeComm(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r != utf8.RuneError && !unicode.IsPrint(r) {
			r = '?'
		}
		if b.Len()+utf8.RuneLen(r) > maxComm {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func emptyTop() Top { return Top{CPU: []Proc{}, RSS: []Proc{}} }

// top orders by cpu, ties to the larger process, and by memory, ties to the busier one, then by pid
func top(all []sample) Top {
	t := emptyTop()
	known := make([]sample, 0, len(all))
	for _, s := range all {
		if s.known {
			known = append(known, s)
		}
	}
	slices.SortFunc(known, func(a, b sample) int {
		return cmp.Or(cmp.Compare(b.CPU, a.CPU), cmp.Compare(b.RSS, a.RSS), cmp.Compare(a.PID, b.PID))
	})
	for _, s := range known[:min(len(known), topN)] {
		t.CPU = append(t.CPU, s.Proc)
	}
	slices.SortFunc(all, func(a, b sample) int {
		return cmp.Or(cmp.Compare(b.RSS, a.RSS), cmp.Compare(b.CPU, a.CPU), cmp.Compare(a.PID, b.PID))
	})
	for _, s := range all[:min(len(all), topN)] {
		t.RSS = append(t.RSS, s.Proc)
	}
	return t
}
