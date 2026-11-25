package core

import (
	"context"
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/discovery"
)

// core's tests run real boards: their devices must not be advertised on the host's network
func init() {
	newDiscovery = func(ctx context.Context, info discovery.Info, path string, log *slog.Logger, _ discovery.AdvertiseFunc) *discovery.Capability {
		return discovery.NewCapabilityWith(ctx, info, path, log, func(ctx context.Context, _ discovery.Info, _ *slog.Logger, ready func()) error {
			ready()
			<-ctx.Done()
			return nil
		})
	}
}

// SetCommandTimeout shortens the deadline each command runs under.
func SetCommandTimeout(s *Sync, d time.Duration) { s.commandTimeout = d }

// SetRecheckEvery shortens how often the level goes back on the hardware while media plays; set it before StartAudio.
func SetRecheckEvery(s *Sync, d time.Duration) { s.recheckEvery = d }

// SetGrantClock is the clock session grants expire by.
func SetGrantClock(s *Sync, now func() time.Time) { s.grants.now = now }
