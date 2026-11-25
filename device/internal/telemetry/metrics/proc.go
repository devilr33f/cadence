package metrics

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"strconv"
	"strings"
)

const (
	// the most of a /proc file read; the kernel's are far smaller
	maxProcFile = 64 << 10
	// cores beyond this many are not reported one by one
	maxCores = 256
	// a sensor file holds one number
	maxSensorFile = 64
)

var errMalformed = errors.New("metrics: malformed source")

// readCapped reads name whole, refusing one over max bytes
func readCapped(fsys fs.FS, name string, maxBytes int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readAllCapped(f, maxBytes)
}

func readAllCapped(r io.Reader, maxBytes int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, errMalformed
	}
	return b, nil
}

// cpuTimes are a line of /proc/stat, in ticks: the busy ones and all of them
type cpuTimes struct{ busy, total uint64 }

type coreTimes struct {
	id string
	t  cpuTimes
}

type cpuStat struct {
	all   cpuTimes
	cores []coreTimes
}

// readStat reads the cpu lines /proc/stat begins with: the aggregate, then one per core
func readStat(fsys fs.FS) (cpuStat, error) {
	f, err := fsys.Open("stat")
	if err != nil {
		return cpuStat{}, err
	}
	defer func() { _ = f.Close() }()
	var st cpuStat
	seen := false
	sc := bufio.NewScanner(io.LimitReader(f, maxProcFile))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			break
		}
		t, ok := parseCPU(fields[1:])
		if !ok {
			return cpuStat{}, errMalformed
		}
		switch id := strings.TrimPrefix(fields[0], "cpu"); {
		case id == "":
			st.all, seen = t, true
		case len(st.cores) < maxCores:
			st.cores = append(st.cores, coreTimes{id: id, t: t})
		}
	}
	if err := sc.Err(); err != nil {
		return cpuStat{}, err
	}
	if !seen {
		return cpuStat{}, errMalformed
	}
	return st, nil
}

// parseCPU takes user nice system idle iowait irq softirq steal (the kernels before iowait have four); guest time is
// already in user's, so it is left out. idle and iowait are idle
func parseCPU(fields []string) (cpuTimes, bool) {
	if len(fields) < 4 {
		return cpuTimes{}, false
	}
	var t cpuTimes
	for i, f := range fields[:min(len(fields), 8)] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return cpuTimes{}, false
		}
		t.total += v
		if i != 3 && i != 4 {
			t.busy += v
		}
	}
	return t, true
}

// utilization is the busy share of the ticks from a to b; none when no tick passed, or when the counters went back
// (back)
func utilization(a, b cpuTimes) (u float64, ok, back bool) {
	switch {
	case b.total < a.total || b.busy < a.busy || b.busy-a.busy > b.total-a.total:
		return 0, false, true
	case b.total == a.total:
		return 0, false, false
	}
	return float64(b.busy-a.busy) / float64(b.total-a.total), true, false
}

// readLoad reads the 1, 5 and 15 minute load averages
func readLoad(fsys fs.FS) ([3]float64, error) {
	b, err := readCapped(fsys, "loadavg", maxProcFile)
	if err != nil {
		return [3]float64{}, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return [3]float64{}, errMalformed
	}
	var out [3]float64
	for i := range out {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
			return [3]float64{}, errMalformed
		}
		out[i] = v
	}
	return out, nil
}

type memory struct{ total, available uint64 }

// readMemory reads MemTotal and MemAvailable in bytes
func readMemory(fsys fs.FS) (memory, error) {
	b, err := readCapped(fsys, "meminfo", maxProcFile)
	if err != nil {
		return memory{}, err
	}
	var m memory
	var haveTotal, haveAvailable bool
	for line := range strings.Lines(string(b)) {
		key, rest, ok := strings.Cut(line, ":")
		if !ok || key != "MemTotal" && key != "MemAvailable" {
			continue
		}
		v, ok := memValue(rest)
		if !ok {
			return memory{}, errMalformed
		}
		if key == "MemTotal" {
			m.total, haveTotal = v, true
		} else {
			m.available, haveAvailable = v, true
		}
	}
	if !haveTotal || !haveAvailable || m.available > m.total {
		return memory{}, errMalformed
	}
	return m, nil
}

// memValue is "<n> kB" in bytes
func memValue(s string) (uint64, bool) {
	fields := strings.Fields(s)
	if len(fields) != 2 || fields[1] != "kB" {
		return 0, false
	}
	v, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || v > math.MaxUint64/1024 {
		return 0, false
	}
	return v * 1024, true
}

// readUptime is the seconds since boot
func readUptime(fsys fs.FS) (float64, error) {
	b, err := readCapped(fsys, "uptime", maxProcFile)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, errMalformed
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, errMalformed
	}
	return v, nil
}

// readTemperature reads a sensor in milli-degrees celsius as degrees
func readTemperature(path string) (float64, error) {
	f, err := os.Open(path) //nolint:gosec // the board's sensor
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	b, err := readAllCapped(f, maxSensorFile)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(string(bytes.TrimSpace(b)), 10, 64)
	if err != nil {
		return 0, errMalformed
	}
	return float64(v) / 1000, nil
}
