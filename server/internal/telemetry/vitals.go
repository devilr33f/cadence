package telemetry

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/events"
)

// vitals (M8-D3)
const (
	// a device's vitals reach the feed at most once in this long
	vitalsEvery = 30 * time.Second
	// the route answers vitals stored no longer ago than this
	vitalsFresh = 10 * time.Minute
	// a round names at most this many disks, by mountpoint: a board declares a few
	maxVitalsDisks = 16
)

// the series vitals read; every other one, a process's too, is left out
const (
	seriesCPU         = "system.cpu.utilization"
	seriesMemoryUsed  = "system.memory.used"
	seriesMemoryLimit = "system.memory.limit"
	seriesDiskLimit   = "system.filesystem.limit"
	seriesDiskFree    = "system.filesystem.free"
	seriesTemperature = "system.temperature"
)

// Vitals are a device's cpu, memory, disks and temperature from one stored
// system.metrics round: never a process.
type Vitals struct {
	CPU              float64 `json:"cpu" minimum:"0" maximum:"1" doc:"the utilization of every core, 0–1"`
	MemoryUsedBytes  int64   `json:"memoryUsedBytes" minimum:"0"`
	MemoryLimitBytes int64   `json:"memoryLimitBytes" minimum:"0"`
	// the mountpoints the board declares that reported both sizes, by mountpoint
	Disk         []DiskVitals `json:"disk" nullable:"false" maxItems:"16"`
	TemperatureC *float64     `json:"temperatureC,omitempty" doc:"where the board has a sensor"`
	At           time.Time    `json:"at" doc:"the round's time"`
}

// DiskVitals are one filesystem's sizes.
type DiskVitals struct {
	Mountpoint string `json:"mountpoint"`
	FreeBytes  int64  `json:"freeBytes" minimum:"0"`
	LimitBytes int64  `json:"limitBytes" minimum:"0"`
}

// VitalsDeps are what the vitals read and tell.
type VitalsDeps struct {
	// Bus hears a device_vitals event, at most one a device each 30 s; nil publishes none
	Bus events.Publisher
	Now func() time.Time
	// AfterFunc runs f once d passed; nil: time.AfterFunc
	AfterFunc func(d time.Duration, f func())
}

// VitalsTracker derives each device's vitals from its stored rounds, in
// memory only: its Observe is a MetricStore listener.
type VitalsTracker struct {
	d VitalsDeps

	mu      sync.Mutex
	devices map[string]*deviceVitals
}

// deviceVitals are a device's latest vitals, when they were stored and when
// the last ones went to the feed, on the server's clock; held says the latest
// waits for its 30 s to be up.
type deviceVitals struct {
	latest          Vitals
	seen, published time.Time
	held            bool
}

func NewVitals(d VitalsDeps) *VitalsTracker {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.AfterFunc == nil {
		d.AfterFunc = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	return &VitalsTracker{d: d, devices: map[string]*deviceVitals{}}
}

// Observe takes the rows of one insert, each device's as one round: a round
// with the total cpu and both memory sizes makes the device's vitals.
func (t *VitalsTracker) Observe(rows []MetricRow) {
	byDevice := map[string][]MetricRow{}
	for _, r := range rows {
		if r.DeviceID.Valid {
			id := r.DeviceID.String()
			byDevice[id] = append(byDevice[id], r)
		}
	}
	for id, rs := range byDevice {
		if v, ok := deriveVitals(rs); ok && t.keep(id, v) {
			t.publish(id, v)
		}
	}
}

func (t *VitalsTracker) publish(id string, v Vitals) {
	if t.d.Bus != nil {
		t.d.Bus.Publish(events.Event{Type: events.DeviceVitals, DeviceID: id, At: v.At, Data: v})
	}
}

// keep makes v the device's latest and reports whether it goes to the feed
// now; within 30 s of the last one it is held until they are up, so a round a
// little early waits instead of being lost. A round older than the latest,
// stored within 30 s of it (a backlog), is left out; past them it is taken: a
// device clock set back. Devices with nothing fresh are forgotten meanwhile,
// a deleted one among them.
func (t *VitalsTracker) keep(id string, v Vitals) bool {
	now := t.d.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for other, d := range t.devices {
		if now.Sub(d.seen) > vitalsFresh {
			delete(t.devices, other)
		}
	}
	d := t.devices[id]
	if d == nil {
		d = &deviceVitals{}
		t.devices[id] = d
	}
	if v.At.Before(d.latest.At) && now.Sub(d.seen) < vitalsEvery {
		return false
	}
	d.latest, d.seen = v, now
	wait := vitalsEvery - now.Sub(d.published)
	if d.published.IsZero() || wait <= 0 {
		d.published, d.held = now, false
		return true
	}
	if !d.held {
		d.held = true
		t.d.AfterFunc(wait, func() { t.release(id) })
	}
	return false
}

// release publishes the device's held vitals once their 30 s are up.
func (t *VitalsTracker) release(id string) {
	t.mu.Lock()
	d := t.devices[id]
	if d == nil || !d.held {
		t.mu.Unlock()
		return
	}
	d.published, d.held = t.d.Now(), false
	v := d.latest
	t.mu.Unlock()
	t.publish(id, v)
}

// Latest is the device's vitals stored in the last 10 minutes, false for none.
func (t *VitalsTracker) Latest(deviceID string) (Vitals, bool) {
	now := t.d.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.devices[deviceID]
	if d == nil || now.Sub(d.seen) > vitalsFresh {
		return Vitals{}, false
	}
	return d.latest, true
}

// sample is the latest gauge point of a series in a round.
type sample struct {
	at    time.Time
	value float64
	set   bool
}

func (s *sample) take(r MetricRow) {
	if !s.set || !r.Time.Before(s.at) {
		*s = sample{at: r.Time, value: r.Value, set: true}
	}
}

// deriveVitals reads one device's round: only gauges of the vitals' series,
// the cpu, memory and temperature without attributes, the disks by
// mountpoint; false without the total cpu or either memory size.
func deriveVitals(rows []MetricRow) (Vitals, bool) {
	var cpu, used, limit, temperature sample
	type disk struct{ free, limit sample }
	disks := map[string]*disk{}
	for _, r := range rows {
		if r.Kind != KindGauge {
			continue
		}
		switch r.Name {
		case seriesCPU, seriesMemoryUsed, seriesMemoryLimit, seriesTemperature:
			if !plain(r.Attrs) {
				continue
			}
			switch r.Name {
			case seriesCPU:
				cpu.take(r)
			case seriesMemoryUsed:
				used.take(r)
			case seriesMemoryLimit:
				limit.take(r)
			default:
				temperature.take(r)
			}
		case seriesDiskLimit, seriesDiskFree:
			m, ok := mountpoint(r.Attrs)
			if !ok {
				continue
			}
			d := disks[m]
			if d == nil {
				d = &disk{}
				disks[m] = d
			}
			if r.Name == seriesDiskFree {
				d.free.take(r)
			} else {
				d.limit.take(r)
			}
		}
	}
	if !cpu.set || !used.set || !limit.set {
		return Vitals{}, false
	}
	v := Vitals{CPU: min(max(cpu.value, 0), 1), MemoryUsedBytes: byteCount(used.value), MemoryLimitBytes: byteCount(limit.value),
		Disk: []DiskVitals{}, At: latest(cpu.at, used.at, limit.at)}
	if temperature.set {
		c := temperature.value
		v.TemperatureC, v.At = &c, latest(v.At, temperature.at)
	}
	for _, m := range slices.Sorted(func(yield func(string) bool) {
		for m, d := range disks {
			if d.free.set && d.limit.set && !yield(m) {
				return
			}
		}
	}) {
		if len(v.Disk) == maxVitalsDisks {
			break
		}
		d := disks[m]
		v.Disk = append(v.Disk, DiskVitals{Mountpoint: m, FreeBytes: byteCount(d.free.value), LimitBytes: byteCount(d.limit.value)})
		v.At = latest(v.At, d.free.at, d.limit.at)
	}
	v.At = v.At.UTC()
	return v, true
}

// plain says attrs is an object with no attributes.
func plain(attrs []byte) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(attrs, &m) == nil && m != nil && len(m) == 0
}

// mountpoint is the mountpoint attribute of a filesystem's point.
func mountpoint(attrs []byte) (string, bool) {
	var a struct {
		Mountpoint *string `json:"mountpoint"`
	}
	if json.Unmarshal(attrs, &a) != nil || a.Mountpoint == nil || strings.TrimSpace(*a.Mountpoint) == "" {
		return "", false
	}
	return *a.Mountpoint, true
}

// byteCount is a size in bytes as stored, kept to 0 … MaxInt64.
func byteCount(v float64) int64 {
	switch {
	case v <= 0:
		return 0
	case v >= math.MaxInt64:
		return math.MaxInt64
	}
	return int64(v)
}

func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}
