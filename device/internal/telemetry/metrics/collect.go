package metrics

import (
	"io/fs"
	"os"
	"time"
)

// Options are what a board's collector reads.
type Options struct {
	// the board's /proc
	Proc fs.FS
	// filesystems whose size is reported, read with Statfs (nil: the package's)
	Mounts []string
	Statfs func(path string) (total, free uint64, err error)
	// a file in milli-degrees celsius, "" for none
	TempSensor string
	// cadenced's own counts, which also count the collector's errors
	App *App
	// whether the board's media plays; nil: no media gauge
	MediaPlaying func() bool
	// more series a component keeps (the screen's player), read at each round; nil: none
	Extra func() []Point
	// 0: the system's page size; nil Now: time.Now
	PageSize int
	Now      func() time.Time
}

// Round is one collection: its points, every count since cadenced started among them, and the top processes.
type Round struct {
	At     time.Time
	Points []Point
	Top    Top
}

// Collector reads a round at a time; a source missing or garbled is left out of that round and counted once in
// cadence.metrics_errors. Not safe for concurrent use.
type Collector struct {
	o     Options
	stat  *cpuStat
	procs procs
}

func NewCollector(o Options) *Collector {
	if o.Statfs == nil {
		o.Statfs = Statfs
	}
	if o.PageSize <= 0 {
		o.PageSize = os.Getpagesize()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Collector{o: o, procs: procs{fsys: o.Proc, pageSize: uint64(o.PageSize)}} //nolint:gosec // positive
}

func (c *Collector) Collect() Round {
	r := Round{At: c.o.Now()}
	add := func(name string, v float64, attrs ...Attr) {
		r.Points = append(r.Points, Point{Name: name, Attrs: attrs, Kind: KindGauge, Value: v})
	}
	failed := func(err error) bool {
		if err != nil {
			c.o.App.metricsError()
		}
		return err != nil
	}
	c.cpu(add, failed)
	if load, err := readLoad(c.o.Proc); !failed(err) {
		add("system.cpu.load_average.1m", load[0])
		add("system.cpu.load_average.5m", load[1])
		add("system.cpu.load_average.15m", load[2])
	}
	if m, err := readMemory(c.o.Proc); !failed(err) {
		add("system.memory.limit", float64(m.total))
		add("system.memory.available", float64(m.available))
		add("system.memory.used", float64(m.total-m.available))
	}
	var free []Point
	for _, mount := range c.o.Mounts {
		total, avail, err := c.o.Statfs(mount)
		if failed(err) {
			continue
		}
		at := Attr{"mountpoint", mount}
		add("system.filesystem.limit", float64(total), at)
		free = append(free, Point{Name: "system.filesystem.free", Attrs: []Attr{at}, Kind: KindGauge, Value: float64(avail)})
	}
	r.Points = append(r.Points, free...)
	if c.o.TempSensor != "" {
		if v, err := readTemperature(c.o.TempSensor); !failed(err) {
			add("system.temperature", v)
		}
	}
	if c.o.MediaPlaying != nil {
		v := 0.0
		if c.o.MediaPlaying() {
			v = 1
		}
		add("cadence.media_playing", v)
	}
	if c.o.Extra != nil {
		r.Points = append(r.Points, c.o.Extra()...)
	}
	// the uptime only matters while no round came before
	var uptime float64
	haveUptime := false
	if c.procs.first() {
		var err error
		uptime, err = readUptime(c.o.Proc)
		haveUptime = !failed(err)
	}
	var bad bool
	if r.Top, bad = c.procs.sample(r.At, uptime, haveUptime); bad {
		c.o.App.metricsError()
	}
	// the counts last, so the round's own errors are in it
	r.Points = append(r.Points, c.o.App.Points()...)
	return r
}

// cpu adds the utilization since the round before, total and per core; counters that went back (a core reset, a
// garbled read) give no value and count one error for the round, as a garbled source does
func (c *Collector) cpu(add func(string, float64, ...Attr), failed func(error) bool) {
	st, err := readStat(c.o.Proc)
	if failed(err) {
		c.stat = nil
		return
	}
	prev := c.stat
	c.stat = &st
	if prev == nil {
		return
	}
	back := false
	if u, ok, b := utilization(prev.all, st.all); ok {
		add("system.cpu.utilization", u)
	} else {
		back = b
	}
	for _, core := range st.cores {
		for _, p := range prev.cores {
			if p.id != core.id {
				continue
			}
			if u, ok, b := utilization(p.t, core.t); ok {
				add("system.cpu.utilization", u, Attr{"cpu", core.id})
			} else {
				back = back || b
			}
		}
	}
	if back {
		c.o.App.metricsError()
	}
}

func (a *App) metricsError() {
	if a != nil {
		a.errors.Inc()
	}
}
