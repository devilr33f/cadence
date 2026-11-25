package telemetry_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// nextVitals is the device_vitals the bus holds, as JSON with its device, or
// "" for none
func nextVitals(t *testing.T, sub *events.Subscription) string {
	t.Helper()
	select {
	case e := <-sub.C:
		if e.Type != events.DeviceVitals {
			t.Fatalf("event %s", e.Type)
		}
		v, ok := e.Data.(telemetry.Vitals)
		if !ok || !e.At.Equal(v.At) {
			t.Fatalf("data %T at %v", e.Data, e.At)
		}
		b, err := json.Marshal(e.Data)
		if err != nil {
			t.Fatal(err)
		}
		return e.DeviceID + " " + string(b)
	default:
		return ""
	}
}

// a device's vitals come from each stored system.metrics round: total cpu,
// memory, the disks that report both sizes and the temperature, never a
// process or another series; the feed gets at most one a device each 30 s, a
// round it held back once those are up, the route the latest of the last 10
// minutes (M8-D3)
func TestVitalsFromStoredRounds(t *testing.T) {
	r := newAdminRig(t)
	sub := r.bus.Watch(nil)
	defer sub.Close()
	a, b := r.devA, r.devB
	round := func(at time.Time, cpu float64, temperature bool) []telemetry.MetricRow {
		rows := []telemetry.MetricRow{
			point(a, "system.cpu.utilization", telemetry.KindGauge, `{"cpu":"0"}`, at, 0.9),
			point(a, "system.cpu.utilization", telemetry.KindGauge, `{}`, at, cpu),
			point(a, "system.cpu.load_average.1m", telemetry.KindGauge, `{}`, at, 3),
			point(a, "system.memory.limit", telemetry.KindGauge, `{}`, at, 4000),
			point(a, "system.memory.available", telemetry.KindGauge, `{}`, at, 3000),
			point(a, "system.memory.used", telemetry.KindGauge, `{}`, at, 1000),
			point(a, "system.filesystem.limit", telemetry.KindGauge, `{"mountpoint":"/tmp"}`, at, 100),
			point(a, "system.filesystem.limit", telemetry.KindGauge, `{"mountpoint":"/data"}`, at, 1000),
			point(a, "system.filesystem.free", telemetry.KindGauge, `{"mountpoint":"/tmp"}`, at, 60),
			point(a, "system.filesystem.free", telemetry.KindGauge, `{"mountpoint":"/data"}`, at, 250),
			// a size without its other half is no disk
			point(a, "system.filesystem.free", telemetry.KindGauge, `{"mountpoint":"/orphan"}`, at, 5),
			point(a, "cadence.sessions", telemetry.KindSum, `{"trigger":"wake"}`, at, 7),
			point(a, "process.cpu.utilization", telemetry.KindGauge, `{"comm":"secret-proc","pid":"42"}`, at, 0.5),
		}
		if temperature {
			rows = append(rows, point(a, "system.temperature", telemetry.KindGauge, `{}`, at, 47.5))
		}
		return rows
	}
	const disks = `"disk":[{"mountpoint":"/data","freeBytes":250,"limitBytes":1000},{"mountpoint":"/tmp","freeBytes":60,"limitBytes":100}]`
	first := `{"cpu":0.25,"memoryUsedBytes":1000,"memoryLimitBytes":4000,` + disks + `,"temperatureC":47.5,"at":"2026-11-11T11:59:59Z"}`
	r.setState(a, "system.metrics", `{"topCpu":[{"pid":42,"comm":"secret-proc","cpu":50,"rss":1}],"topRss":[]}`)
	r.insertPoints(round(start.Add(-time.Second), 0.25, true)...)
	if got := nextVitals(t, sub); got != a.String()+" "+first {
		t.Fatalf("first: %s", got)
	}
	if st, _, body := r.get("/api/v1/devices/"+a.String()+"/vitals", haToken); st != 200 || body != first {
		t.Fatalf("route: %d %s", st, body)
	}

	// 10 s on: the route has it, the feed holds it back; another device has its own 30 s
	r.timers.advance(10 * time.Second)
	r.insertPoints(round(start.Add(9*time.Second), 0.5, false)...)
	r.insertPoints(
		point(b, "system.cpu.utilization", telemetry.KindGauge, `{}`, start.Add(9*time.Second), 1),
		point(b, "system.memory.used", telemetry.KindGauge, `{}`, start.Add(9*time.Second), 2),
		point(b, "system.memory.limit", telemetry.KindGauge, `{}`, start.Add(9*time.Second), 3),
	)
	second := `{"cpu":0.5,"memoryUsedBytes":1000,"memoryLimitBytes":4000,` + disks + `,"at":"2026-11-11T12:00:09Z"}`
	if got := nextVitals(t, sub); got != b.String()+` {"cpu":1,"memoryUsedBytes":2,"memoryLimitBytes":3,"disk":[],"at":"2026-11-11T12:00:09Z"}` {
		t.Fatalf("device b: %s", got)
	}
	if got := nextVitals(t, sub); got != "" {
		t.Fatalf("within 30 s: %s", got)
	}
	if st, _, body := r.get("/api/v1/devices/"+a.String()+"/vitals", adminToken); st != 200 || body != second {
		t.Fatalf("route after the second: %d %s", st, body)
	}
	// the one held back goes out once 30 s have passed since the first, not with the next round
	r.timers.advance(20*time.Second - time.Millisecond)
	if got := nextVitals(t, sub); got != "" {
		t.Fatalf("before 30 s: %s", got)
	}
	r.timers.advance(time.Millisecond)
	if got := nextVitals(t, sub); got != a.String()+" "+second {
		t.Fatalf("at 30 s: %s", got)
	}

	// a round without the total cpu (cadenced's first) or the memory gives no vitals, and one older than the latest,
	// right after it, leaves the latest
	r.insertPoints(point(a, "system.memory.used", telemetry.KindGauge, `{}`, start.Add(29*time.Second), 9),
		point(a, "system.memory.limit", telemetry.KindGauge, `{}`, start.Add(29*time.Second), 9))
	r.insertPoints(point(a, "system.cpu.utilization", telemetry.KindGauge, `{}`, start.Add(29*time.Second), 0.1))
	r.insertPoints(round(start.Add(5*time.Second), 0.9, true)...)
	if got := nextVitals(t, sub); got != "" {
		t.Fatalf("a partial or older round: %s", got)
	}
	if _, _, body := r.get("/api/v1/devices/"+a.String()+"/vitals", haToken); body != second {
		t.Fatalf("route after a partial or older round: %s", body)
	}

	// the next round waits its 30 s too
	r.timers.advance(5 * time.Second)
	r.insertPoints(round(start.Add(34*time.Second), 0.75, true)...)
	third := `{"cpu":0.75,"memoryUsedBytes":1000,"memoryLimitBytes":4000,` + disks + `,"temperatureC":47.5,"at":"2026-11-11T12:00:34Z"}`
	if got := nextVitals(t, sub); got != "" {
		t.Fatalf("5 s after the last: %s", got)
	}
	r.timers.advance(25 * time.Second)
	if got := nextVitals(t, sub); got != a.String()+" "+third {
		t.Fatalf("30 s after the last: %s", got)
	}
	if strings.Contains(first+second+third, "secret-proc") {
		t.Fatal("a process reached the vitals")
	}

	// a device clock set back: an older round is the latest once the latest is 30 s old, and goes out at once
	r.timers.advance(30 * time.Second)
	r.insertPoints(round(start.Add(20*time.Second), 0.5, false)...)
	back := `{"cpu":0.5,"memoryUsedBytes":1000,"memoryLimitBytes":4000,` + disks + `,"at":"2026-11-11T12:00:20Z"}`
	if got := nextVitals(t, sub); got != a.String()+" "+back {
		t.Fatalf("a clock set back: %s", got)
	}

	// none in the last 10 minutes, a device with none, an unknown one and a metrics token
	other, err := r.q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "hall", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		other.ID.String():                      `{"error":"no_vitals"}`,
		"00000000-0000-0000-0000-000000000009": `{"error":"not_found"}`,
		"not-a-uuid":                           `{"error":"not_found"}`,
	} {
		if st, _, body := r.get("/api/v1/devices/"+path+"/vitals", haToken); st != 404 || body != want {
			t.Fatalf("%s: %d %s", path, st, body)
		}
	}
	if st, _, body := r.get("/api/v1/devices/"+a.String()+"/vitals", metricsToken); st != 403 || body != `{"error":"forbidden"}` {
		t.Fatalf("metrics token: %d %s", st, body)
	}
	r.clock.Add(10*time.Minute + time.Second)
	if st, _, body := r.get("/api/v1/devices/"+a.String()+"/vitals", haToken); st != 404 || body != `{"error":"no_vitals"}` {
		t.Fatalf("10 minutes on: %d %s", st, body)
	}
}

// what a device sends past the ranges is kept to them: the cpu to 0–1, sizes
// to 0 and above, at most 16 disks by mountpoint, and a series sent as a sum
// is no vitals
func TestVitalsKeptToTheirRanges(t *testing.T) {
	r := newAdminRig(t)
	sub := r.bus.Watch(nil)
	defer sub.Close()
	a, at := r.devA, start
	rows := []telemetry.MetricRow{
		point(a, "system.cpu.utilization", telemetry.KindGauge, `{}`, at, 7),
		point(a, "system.memory.used", telemetry.KindGauge, `{}`, at, -5),
		point(a, "system.memory.limit", telemetry.KindGauge, `{}`, at, 1e300),
		point(a, "system.temperature", telemetry.KindSum, `{}`, at, 40),
	}
	for i := range 20 {
		m := `{"mountpoint":"/m` + string(rune('a'+i)) + `"}`
		rows = append(rows, point(a, "system.filesystem.limit", telemetry.KindGauge, m, at, 2),
			point(a, "system.filesystem.free", telemetry.KindGauge, m, at, 1))
	}
	r.insertPoints(rows...)
	var v telemetry.Vitals
	got := nextVitals(t, sub)
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got, a.String()+" ")), &v); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	if v.CPU != 1 || v.MemoryUsedBytes != 0 || v.MemoryLimitBytes != 1<<63-1 || v.TemperatureC != nil || len(v.Disk) != 16 ||
		v.Disk[0].Mountpoint != "/ma" || v.Disk[15].Mountpoint != "/mp" {
		t.Fatalf("vitals %s", got)
	}
}
