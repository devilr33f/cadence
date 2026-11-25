package app

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/presence"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// runPresence loads busy mode's stored signals and expires them, and with a
// home follows the global busyEntities on it; poke says the global settings
// changed.
func runPresence(ctx context.Context, s *presence.Service, home *ha.Home, global func(context.Context) (settings.Values, error)) (poke, stop func()) {
	stopStored := s.Run(ctx)
	if home == nil {
		return func() {}, stopStored
	}
	poke, stopHA := s.RunHA(ctx, presence.HADeps{Home: home, Entities: func(ctx context.Context) ([]settings.BusyEntity, error) {
		v, err := global(ctx)
		return v.BusyEntities, err
	}})
	return poke, func() {
		stopHA()
		stopStored()
	}
}

// busyNow is the reporter's view of busy mode.
func busyNow(s *presence.Service) func(context.Context, string) bool {
	return func(ctx context.Context, id string) bool {
		busy, _ := s.Busy(ctx, id)
		return busy
	}
}
