package ringsounds

import (
	"context"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// Refs reads which sounds each device's alarms and reminders name now, by device.
type Refs func(ctx context.Context) (map[string][]string, error)

// Collect marks the sounds refs names as in use now and deletes the ones nothing named for 7 days.
func (s *Service) Collect(ctx context.Context, refs Refs) {
	now := s.d.Now()
	named, err := refs(ctx)
	if err != nil {
		// nothing is deleted on a partial picture
		s.d.Log.Warn("ring_sounds_gc_failed", "code", "refs")
		return
	}
	for deviceID, ids := range named {
		dev, ok := parseDevice(deviceID)
		if !ok || len(ids) == 0 {
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, storeTimeout)
		err := s.d.Q.TouchRingSounds(tctx, db.TouchRingSoundsParams{Now: stamp(now), DeviceID: dev, Ids: ids})
		cancel()
		if err != nil {
			s.d.Log.Warn("ring_sounds_gc_failed", "code", "store")
			return
		}
	}
	dctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	n, err := s.d.Q.DeleteStaleRingSounds(dctx, stamp(now.Add(-unusedFor)))
	if err != nil {
		s.d.Log.Warn("ring_sounds_gc_failed", "code", "store")
		return
	}
	if n > 0 {
		s.d.Log.Info("ring_sounds_deleted", "count", n)
	}
}

// Run collects every interval until ctx ends or stop is called.
func (s *Service) Run(ctx context.Context, every time.Duration, refs Refs) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.Collect(ctx, refs)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
