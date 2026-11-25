package app

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

// a device's alarms change reaches every linked screen, a link its own screen, a speaker none; a device going offline
// sends them again, and a deleted one is forgotten
func TestAlarmsPushReachesScreens(t *testing.T) {
	var mu sync.Mutex
	sent := map[string]int{}
	var forgot []string
	p := (&alarmsPush{
		send: func(dev string, env *cadencev2.Envelope) error {
			if env.GetAlarmsSummary() == nil {
				t.Errorf("sent %v", env)
			}
			mu.Lock()
			sent[dev]++
			mu.Unlock()
			return nil
		},
		isScreen: func(_ context.Context, dev string) bool { return dev != "speaker" },
		screens:  func(context.Context) []string { return []string{"tv", "tablet", "speaker"} },
		summary: func(context.Context, string) (*cadencev2.AlarmsSummary, error) {
			return &cadencev2.AlarmsSummary{}, nil
		},
		log: slog.New(slog.DiscardHandler),
	}).init()
	bus := events.NewBus(8)
	stop := p.Run(context.Background(), bus, func(d string) {
		mu.Lock()
		forgot = append(forgot, d)
		mu.Unlock()
	})
	defer stop()
	waitFor := func(want map[string]int) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			mu.Lock()
			ok := len(sent) == len(want)
			for k, v := range want {
				ok = ok && sent[k] == v
			}
			mu.Unlock()
			if ok {
				return
			}
			if time.Now().After(deadline) {
				mu.Lock()
				defer mu.Unlock()
				t.Fatalf("sent %v, want %v", sent, want)
			}
		}
	}
	p.Linked("tv")
	waitFor(map[string]int{"tv": 1})
	p.Changed("speaker")
	p.Changed("tv")
	waitFor(map[string]int{"tv": 2, "tablet": 1})
	bus.Publish(events.Event{Type: events.DeviceDeleted, DeviceID: "speaker"})
	waitFor(map[string]int{"tv": 3, "tablet": 2})
	mu.Lock()
	defer mu.Unlock()
	if len(forgot) != 1 || forgot[0] != "speaker" {
		t.Fatalf("forgot %v", forgot)
	}
}
