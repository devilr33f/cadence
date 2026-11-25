package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/alarmview"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

// a ring wants its overlay soon, a burst of changes one summary
const alarmsDebounce = 150 * time.Millisecond

// alarmsPush sends each linked screen the household's AlarmsSummary: at its link's start, and to every screen after
// any device's alarms state changed, or a device came, went, was renamed or deleted (a ring of a device that went
// offline shows nowhere). Nothing of it is logged but ids.
type alarmsPush struct {
	send     func(deviceID string, env *cadencev2.Envelope) error
	isScreen func(ctx context.Context, deviceID string) bool
	screens  func(ctx context.Context) []string
	summary  func(ctx context.Context, screen string) (*cadencev2.AlarmsSummary, error)
	log      *slog.Logger

	mu   sync.Mutex
	due  map[string]bool
	kick chan struct{}
}

func (p *alarmsPush) init() *alarmsPush {
	p.due, p.kick = map[string]bool{}, make(chan struct{}, 1)
	return p
}

// Linked has the screen's summary sent soon.
func (p *alarmsPush) Linked(dev string) { p.mark(dev) }

// Changed has every linked screen's summary sent soon: a screen shows every device's alarms.
func (p *alarmsPush) Changed(string) { p.mark("") }

func (p *alarmsPush) mark(dev string) {
	p.mu.Lock()
	p.due[dev] = true
	p.mu.Unlock()
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// the device events that change what a summary shows
var alarmsWatched = map[string]bool{
	events.DeviceOnline: true, events.DeviceOffline: true, events.DeviceRenamed: true, events.DeviceDeleted: true,
}

// Run sends the summaries due until ctx ends, and follows the bus's device events; forget hears a deleted device.
func (p *alarmsPush) Run(ctx context.Context, bus *events.Bus, forget func(string)) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	sub := bus.Watch(func(e events.Event) bool { return alarmsWatched[e.Type] })
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-sub.C:
				if e.Type == events.DeviceDeleted && forget != nil {
					forget(e.DeviceID)
				}
				p.Changed(e.DeviceID)
			case <-sub.Drops():
				p.Changed("")
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.kick:
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(alarmsDebounce):
			}
			p.flush(ctx)
		}
	}()
	return func() { cancel(); wg.Wait(); sub.Close() }
}

func (p *alarmsPush) flush(ctx context.Context) {
	p.mu.Lock()
	due := p.due
	p.due = map[string]bool{}
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	devs := map[string]bool{}
	for d := range due {
		if d != "" {
			devs[d] = true
		}
	}
	if due[""] {
		for _, d := range p.screens(ctx) {
			devs[d] = true
		}
	}
	for d := range devs {
		if !p.isScreen(ctx, d) {
			continue
		}
		sum, err := p.summary(ctx, d)
		if err != nil {
			p.log.Warn("alarms_summary_failed", "device_id", d)
			continue
		}
		_ = p.send(d, &cadencev2.Envelope{Body: &cadencev2.Envelope_AlarmsSummary{AlarmsSummary: sum}})
	}
}

// alarmDevices lists the enrolled devices as the alarms summaries read them
func alarmDevices(list func(ctx context.Context) ([]enroll.Listed, error)) func(ctx context.Context) ([]alarmview.Device, error) {
	return func(ctx context.Context) ([]alarmview.Device, error) {
		rows, err := list(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]alarmview.Device, 0, len(rows))
		for _, r := range rows {
			if r.Status == "enrolled" {
				out = append(out, alarmview.Device{ID: r.ID, Name: r.Name, Room: r.Room})
			}
		}
		return out, nil
	}
}
