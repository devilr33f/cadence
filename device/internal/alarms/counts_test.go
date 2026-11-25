package alarms_test

import (
	"maps"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/input"
)

type ringCounts struct {
	mu             sync.Mutex
	began, stopped map[string]int
}

func (c *ringCounts) RingBegan(kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.began[kind]++
}

func (c *ringCounts) RingStopped(kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped[kind]++
}

func (c *ringCounts) want(t *testing.T, what string, began, stopped map[string]int) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !maps.Equal(c.began, began) || !maps.Equal(c.stopped, stopped) {
		t.Fatalf("%s: began %v stopped %v, want %v %v", what, c.began, c.stopped, began, stopped)
	}
}

// each ring counts by its kind when it begins, one that waited behind another too, and again when it stops, whatever
// stops it
func TestRingsAndStopsCountedByKind(t *testing.T) {
	counts := &ringCounts{began: map[string]int{}, stopped: map[string]int{}}
	g := newRig(t, at(26, 6, 59, 0), 30, func(o *alarms.RingerOptions) { o.Counts = counts })
	now := at(26, 6, 59, 0)
	g.mustSetAlarm(g.book, now, alarms.Alarm{ID: "weekdays", Hour: 7, Days: weekdays, Level: 80})
	g.mustSetTimer(g.book, now, "tea", 60)
	start := at(26, 7, 0, 0)
	g.step(start)
	counts.want(t, "both due", map[string]int{"timer": 1}, map[string]int{})
	g.sound("ring-1", start)

	g.clock.set(start.Add(5 * time.Second))
	g.r.StopRing(input.Action)
	counts.want(t, "the timer stopped", map[string]int{"timer": 1, "alarm": 1}, map[string]int{"timer": 1})
	g.sound("ring-2", start.Add(5*time.Second))
	g.step(start.Add(5*time.Second + alarms.AlarmLimit))
	counts.want(t, "the alarm timed out", map[string]int{"timer": 1, "alarm": 1}, map[string]int{"timer": 1, "alarm": 1})
}
