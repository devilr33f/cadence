package metrics_test

import (
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func attr(k, v string) metrics.Attr { return metrics.Attr{Key: k, Value: v} }

// every count cadenced keeps is a cumulative sum from its start, reported from zero
func TestAppCounts(t *testing.T) {
	app := metrics.NewApp(t0)
	r := metrics.Round{Points: app.Points()}
	for _, p := range r.Points {
		if p.Kind != metrics.KindSum || p.Count != 0 {
			t.Fatalf("at start %+v", p)
		}
	}
	count(t, r, "cadence.sessions", attr("trigger", "follow_up"))
	count(t, r, "cadence.ring_stops", attr("kind", "timer"))
	absent(t, r, "cadence.logs_dropped")

	app.SessionBegan(cadencev2.Trigger_TRIGGER_WAKE)
	app.SessionBegan(cadencev2.Trigger_TRIGGER_WAKE)
	app.SessionBegan(cadencev2.Trigger_TRIGGER_BUTTON)
	app.SessionBegan(cadencev2.Trigger_TRIGGER_FOLLOW_UP)
	// a device begins no other
	app.SessionBegan(cadencev2.Trigger_TRIGGER_EVENT)
	app.WakeHit()
	app.CommandWord(true)
	app.CommandWord(false)
	app.CommandWord(false)
	app.RingBegan("alarm")
	app.RingBegan("timer")
	app.RingBegan("timer")
	app.RingBegan("other")
	app.RingStopped("timer")
	app.AudiodRestarted()
	app.ObserveLogs(func() logs.Stats { return logs.Stats{Dropped: 7, Rejected: 2, Cut: 3, RingBytes: 99} })

	r = metrics.Round{Points: app.Points()}
	for _, w := range []struct {
		name  string
		attrs []metrics.Attr
		want  uint64
	}{
		{"cadence.sessions", []metrics.Attr{attr("trigger", "wake")}, 2},
		{"cadence.sessions", []metrics.Attr{attr("trigger", "button")}, 1},
		{"cadence.sessions", []metrics.Attr{attr("trigger", "follow_up")}, 1},
		{"cadence.wake_hits", nil, 1},
		{"cadence.commands", nil, 1},
		{"cadence.commands_ignored", nil, 2},
		{"cadence.rings", []metrics.Attr{attr("kind", "alarm")}, 1},
		{"cadence.rings", []metrics.Attr{attr("kind", "timer")}, 2},
		{"cadence.ring_stops", []metrics.Attr{attr("kind", "alarm")}, 0},
		{"cadence.ring_stops", []metrics.Attr{attr("kind", "timer")}, 1},
		{"cadence.audiod_restarts", nil, 1},
		{"cadence.logs_dropped", nil, 7},
		{"cadence.logs_rejected", nil, 2},
		{"cadence.logs_cut", nil, 3},
		{"cadence.metrics_errors", nil, 0},
		{"cadence.metrics_dropped", nil, 0},
	} {
		if got := count(t, r, w.name, w.attrs...); got != w.want {
			t.Errorf("%s %v = %d, want %d", w.name, w.attrs, got, w.want)
		}
	}
	for _, p := range r.Points {
		if p.Name == "cadence.sessions" && p.Attrs[0].Value == "event" || p.Name == "cadence.rings" && p.Attrs[0].Value == "other" {
			t.Fatalf("an unknown value made a series: %+v", p)
		}
	}
	if app.Start() != t0 {
		t.Fatalf("start %v", app.Start())
	}
}

// a component without counts is handed a nil app
func TestNilAppCountsNothing(t *testing.T) {
	var app *metrics.App
	app.SessionBegan(cadencev2.Trigger_TRIGGER_WAKE)
	app.WakeHit()
	app.CommandWord(true)
	app.RingBegan("alarm")
	app.RingStopped("alarm")
	app.AudiodRestarted()
	app.ObserveLogs(func() logs.Stats { return logs.Stats{Dropped: 1} })
	if app.Points() != nil {
		t.Fatal("points from a nil app")
	}
	if !app.Start().IsZero() {
		t.Fatal("a start for a nil app")
	}
}

func TestCountsFromManyGoroutines(t *testing.T) {
	app := metrics.NewApp(t0)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 1000 {
				app.WakeHit()
				app.SessionBegan(cadencev2.Trigger_TRIGGER_BUTTON)
				_ = app.Points()
			}
		})
	}
	wg.Wait()
	r := metrics.Round{Points: app.Points()}
	if n := count(t, r, "cadence.wake_hits"); n != 8000 {
		t.Fatalf("wake hits %d", n)
	}
	if n := count(t, r, "cadence.sessions", attr("trigger", "button")); n != 8000 {
		t.Fatalf("sessions %d", n)
	}
}

// the registry keeps one series per name and attributes
func TestRegistrySeries(t *testing.T) {
	var reg metrics.Registry
	a := reg.Counter("x", attr("k", "1"))
	b := reg.Counter("x", attr("k", "1"))
	c := reg.Counter("x", attr("k", "2"))
	a.Inc()
	b.Add(2)
	c.Inc()
	reg.CounterFunc("y", func() uint64 { return 42 })
	r := metrics.Round{Points: reg.Points()}
	if a != b || count(t, r, "x", attr("k", "1")) != 3 || count(t, r, "x", attr("k", "2")) != 1 || count(t, r, "y") != 42 {
		t.Fatalf("points %v", r.Points)
	}
	if len(r.Points) != 3 {
		t.Fatalf("points %v", r.Points)
	}
}
