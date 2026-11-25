package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

type modeGetter interface {
	Get(ctx context.Context, name string) (agent.Mode, error)
}

// describedModes loads the modes agent_mode's description lists. A missing row
// is logged by name and skipped: the tool still resolves modes at call time.
// Any other failure is returned.
func describedModes(ctx context.Context, modes modeGetter, names []string, log *slog.Logger) ([]agent.Mode, error) {
	var out []agent.Mode
	for _, name := range names {
		m, err := modes.Get(ctx, name)
		switch {
		case errors.Is(err, agent.ErrModeNotFound):
			log.Error("agent_mode_missing", "mode", name)
		case err != nil:
			return nil, err
		default:
			out = append(out, m)
		}
	}
	return out, nil
}

// globalAllowedModes reads the allowed modes of the global settings, the
// defaults included.
func globalAllowedModes(prefs *settings.Service) func(ctx context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		v, err := prefs.EffectiveGlobal(ctx)
		return v.AllowedModes, err
	}
}

// modeList holds the modes agent_mode's description lists, the global allowed
// modes, so that each turn reads them without a query.
type modeList struct {
	modes   modeGetter
	allowed func(ctx context.Context) ([]string, error)
	log     *slog.Logger
	cur     atomic.Pointer[[]agent.Mode]
}

// Modes is the list as last loaded.
func (l *modeList) Modes() []agent.Mode {
	if p := l.cur.Load(); p != nil {
		return *p
	}
	return nil
}

// load replaces the list with the modes as they are now and says whether it
// did; a failed read keeps the list it had.
func (l *modeList) load(ctx context.Context) bool {
	names, err := l.allowed(ctx)
	var list []agent.Mode
	if err == nil {
		list, err = describedModes(ctx, l.modes, names, l.log)
	}
	if err != nil {
		if ctx.Err() == nil {
			l.log.Warn("agent_modes_unread")
		}
		return false
	}
	l.cur.Store(&list)
	return true
}

// retry is the wait before a failed load is tried again: base, doubling up to
// limit.
type retry struct{ base, limit time.Duration }

var modesRetry = retry{base: time.Second, limit: time.Minute}

// watchModes loads the list, then again after each poke (a mode put or
// delete, a global settings write) until stop; pokes that arrive during a
// load are served by one more. A failed load is tried again on its own
// (modesRetry), so a store down at startup leaves the list empty only until it
// answers.
func watchModes(ctx context.Context, modes modeGetter, allowed func(ctx context.Context) ([]string, error), log *slog.Logger) (list *modeList, poke, stop func()) {
	return watchModesRetrying(ctx, modes, allowed, log, modesRetry)
}

func watchModesRetrying(ctx context.Context, modes modeGetter, allowed func(ctx context.Context) ([]string, error), log *slog.Logger, r retry) (list *modeList, poke, stop func()) {
	list = &modeList{modes: modes, allowed: allowed, log: log}
	loaded := list.load(ctx)
	ctx, cancel := context.WithCancel(ctx)
	pokes := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		wait := r.base
		timer := time.NewTimer(wait)
		defer timer.Stop()
		if loaded {
			timer.Stop()
		} else {
			wait = min(2*wait, r.limit)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-pokes:
			case <-timer.C:
			}
			timer.Stop()
			if list.load(ctx) {
				wait = r.base
				continue
			}
			timer.Reset(wait)
			wait = min(2*wait, r.limit)
		}
	}()
	poke = func() {
		select {
		case pokes <- struct{}{}:
		default:
		}
	}
	var once sync.Once
	return list, poke, func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}
