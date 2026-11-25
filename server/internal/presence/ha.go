package presence

import (
	"context"
	"slices"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// Home is Home Assistant's live states; *ha.Home is one.
type Home interface {
	// Watch is the state of each of ids that has one while the connection is
	// up, with a channel closed at the next change, a disconnect included.
	Watch(ids []string) (up bool, states map[string]string, next <-chan struct{})
}

// HADeps are what the Home Assistant source reads: the live states, and the
// global busyEntities that map them to signals.
type HADeps struct {
	Home     Home
	Entities func(ctx context.Context) ([]settings.BusyEntity, error)
}

// haRetry is how soon an unreadable busyEntities is read again.
const haRetry = 30 * time.Second

// RunHA keeps a signal for each busy entity whose state matches, source
// ha:<entity> on every device, with no expiry: it lasts while the state holds
// and Home Assistant is connected, and goes when it disconnects. poke says the
// settings changed. It returns a stop that waits for it and clears them.
func (s *Service) RunHA(ctx context.Context, d HADeps) (poke func(), stop func()) {
	kick := make(chan struct{}, 1)
	poke = func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.watchHA(rctx, d, kick)
	}()
	return poke, func() {
		cancel()
		<-done
		s.setLive(context.WithoutCancel(ctx), map[string]Signal{})
	}
}

func (s *Service) watchHA(ctx context.Context, d HADeps, kick <-chan struct{}) {
	var entities []settings.BusyEntity
	read := func() bool {
		list, err := d.Entities(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.d.Log.Error("busy_failed", "op", "ha_settings")
			}
			return false
		}
		entities = list
		return true
	}
	fresh := read()
	for {
		ids := make([]string, 0, len(entities))
		for _, e := range entities {
			if !slices.Contains(ids, e.Entity) {
				ids = append(ids, e.Entity)
			}
		}
		up, states, next := d.Home.Watch(ids)
		s.setLive(ctx, haSignals(entities, up, states))
		var retry <-chan time.Time
		if !fresh {
			retry = time.After(haRetry)
		}
		select {
		case <-ctx.Done():
			return
		case <-next:
		case <-kick:
			fresh = read()
		case <-retry:
			fresh = read()
		}
	}
}

// haSignals are the signals the states make: the first busy entity that
// matches an entity's state gives its reason.
func haSignals(entities []settings.BusyEntity, up bool, states map[string]string) map[string]Signal {
	out := map[string]Signal{}
	if !up {
		return out
	}
	for _, e := range entities {
		src := haPrefix + e.Entity
		if _, ok := out[src]; ok {
			continue
		}
		if st, ok := states[e.Entity]; ok && st == e.State {
			out[src] = Signal{Source: src, Reason: e.Reason}
		}
	}
	return out
}
