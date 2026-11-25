// Package metrics collects a board's system metrics from /proc and the board's mounts and sensor, cadenced's own
// counts and the top processes, and exports a round of them as one OTLP request (M6b-D5). A process's command line
// and environment are never read (rule 8).
package metrics

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// Attr is one attribute of a series.
type Attr struct{ Key, Value string }

// Kind is how a point is exported.
type Kind uint8

const (
	// KindGauge is a value as it stands at the round
	KindGauge Kind = iota
	// KindSum is a monotonic count, cumulative since cadenced started
	KindSum
)

// Point is one series' value in a round: Value for a gauge, Count for a sum.
type Point struct {
	Name  string
	Attrs []Attr
	Kind  Kind
	Value float64
	Count uint64
}

// Counter is a monotonic count; safe for concurrent use.
type Counter struct{ n atomic.Uint64 }

func (c *Counter) Inc()         { c.n.Add(1) }
func (c *Counter) Add(n uint64) { c.n.Add(n) }
func (c *Counter) Load() uint64 { return c.n.Load() }

// Registry holds counts as sums, in the order they were registered: counters the components add to and counts kept
// elsewhere, read at each round. The zero value is ready; safe for concurrent use.
type Registry struct {
	mu     sync.Mutex
	series []series
}

type series struct {
	name  string
	attrs []Attr
	c     *Counter
	read  func() uint64
}

// Counter is the series of name and attrs, made on the first call.
func (r *Registry) Counter(name string, attrs ...Attr) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.series {
		if s.c != nil && s.name == name && slices.Equal(s.attrs, attrs) {
			return s.c
		}
	}
	c := &Counter{}
	r.series = append(r.series, series{name: name, attrs: slices.Clone(attrs), c: c, read: c.Load})
	return c
}

// CounterFunc reports what read returns, a count kept elsewhere that only grows, as the sum name.
func (r *Registry) CounterFunc(name string, read func() uint64, attrs ...Attr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.series = append(r.series, series{name: name, attrs: slices.Clone(attrs), read: read})
}

// Points reads every series.
func (r *Registry) Points() []Point {
	r.mu.Lock()
	ss := slices.Clone(r.series)
	r.mu.Unlock()
	out := make([]Point, 0, len(ss))
	for _, s := range ss {
		out = append(out, Point{Name: s.name, Attrs: s.attrs, Kind: KindSum, Count: s.read()})
	}
	return out
}

// the values an attribute takes; any other is no series
var (
	triggers = []struct {
		t    cadencev2.Trigger
		name string
	}{
		{cadencev2.Trigger_TRIGGER_WAKE, "wake"}, {cadencev2.Trigger_TRIGGER_BUTTON, "button"},
		{cadencev2.Trigger_TRIGGER_FOLLOW_UP, "follow_up"},
	}
	ringKinds = []string{"alarm", "timer"}
)

// App is cadenced's own counts (M6b-D5), cumulative since it started. The components count into it through narrow
// interfaces of their own, which it implements; a nil App counts nothing. Every method must stay nil-safe: an offline
// device's boards pass a nil App through those interfaces.
type App struct {
	reg   Registry
	start time.Time

	sessions                            map[cadencev2.Trigger]*Counter
	rings, ringStops                    map[string]*Counter
	wakeHits, commands, commandsIgnored *Counter
	audiodRestarts, errors, dropped     *Counter
}

// NewApp starts every count at zero; start is when cadenced started.
func NewApp(start time.Time) *App {
	a := &App{
		start: start, sessions: map[cadencev2.Trigger]*Counter{}, rings: map[string]*Counter{}, ringStops: map[string]*Counter{},
	}
	for _, t := range triggers {
		a.sessions[t.t] = a.reg.Counter("cadence.sessions", Attr{"trigger", t.name})
	}
	a.wakeHits = a.reg.Counter("cadence.wake_hits")
	a.commands = a.reg.Counter("cadence.commands")
	a.commandsIgnored = a.reg.Counter("cadence.commands_ignored")
	for _, k := range ringKinds {
		a.rings[k] = a.reg.Counter("cadence.rings", Attr{"kind", k})
	}
	for _, k := range ringKinds {
		a.ringStops[k] = a.reg.Counter("cadence.ring_stops", Attr{"kind", k})
	}
	a.audiodRestarts = a.reg.Counter("cadence.audiod_restarts")
	a.errors = a.reg.Counter("cadence.metrics_errors")
	a.dropped = a.reg.Counter("cadence.metrics_dropped")
	return a
}

// Start is when the sums began.
func (a *App) Start() time.Time {
	if a == nil {
		return time.Time{}
	}
	return a.start
}

// SessionBegan counts a session a wake, a press or a follow-up began.
func (a *App) SessionBegan(t cadencev2.Trigger) {
	if a != nil {
		inc(a.sessions[t])
	}
}

// WakeHit counts each time audiod's wake engine heard the wake word, whether a session began or not.
func (a *App) WakeHit() {
	if a != nil {
		a.wakeHits.Inc()
	}
}

// CommandWord counts a command word acted on, or ignored.
func (a *App) CommandWord(acted bool) {
	switch {
	case a == nil:
	case acted:
		a.commands.Inc()
	default:
		a.commandsIgnored.Inc()
	}
}

// RingBegan counts a ring of kind (alarm or timer) that began sounding.
func (a *App) RingBegan(kind string) {
	if a != nil {
		inc(a.rings[kind])
	}
}

// RingStopped counts a ring of kind that stopped, whatever stopped it.
func (a *App) RingStopped(kind string) {
	if a != nil {
		inc(a.ringStops[kind])
	}
}

// AudiodRestarted counts audiod started again after it died.
func (a *App) AudiodRestarted() {
	if a != nil {
		a.audiodRestarts.Inc()
	}
}

// ObserveLogs reports the log pipeline's drops, refusals and cut lines with each round; call it once.
func (a *App) ObserveLogs(stats func() logs.Stats) {
	if a == nil {
		return
	}
	a.reg.CounterFunc("cadence.logs_dropped", func() uint64 { return stats().Dropped })
	a.reg.CounterFunc("cadence.logs_rejected", func() uint64 { return stats().Rejected })
	a.reg.CounterFunc("cadence.logs_cut", func() uint64 { return stats().Cut })
}

// Points reads every count.
func (a *App) Points() []Point {
	if a == nil {
		return nil
	}
	return a.reg.Points()
}

func inc(c *Counter) {
	if c != nil {
		c.Inc()
	}
}
