package app

import (
	"context"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/screens"
)

// householdDeps are what the agent's devices note and target resolver read
// (TV plan item 14): the enrolled devices, their capabilities and screen
// state, their settings (the room is haArea until devices carry one, as the
// rooms directory reads it) and what each plays.
type householdDeps struct {
	list     func(ctx context.Context) ([]enroll.Listed, error)
	detail   func(ctx context.Context, id string) (devices.Detail, error)
	settings tools.SettingsFunc
	online   func(id string) bool
	playing  func(id string) (agent.NowPlaying, bool)
	// seen, when set, is when a screen last sent its context (the user's last input there)
	seen func(id string) time.Time
}

// household lists every enrolled device for the agent; an online one's
// capabilities are read at once, beside the others
func household(d householdDeps) agent.Household {
	return func(ctx context.Context) ([]agent.DeviceInfo, error) {
		listed, err := d.list(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]agent.DeviceInfo, 0, len(listed))
		for _, l := range listed {
			if l.Status != "enrolled" {
				continue
			}
			out = append(out, agent.DeviceInfo{ID: l.ID, Name: l.Name, Kind: agent.KindSpeaker})
		}
		var wg sync.WaitGroup
		for i := range out {
			wg.Go(func() { d.fill(ctx, &out[i]) })
		}
		wg.Wait()
		return out, ctx.Err()
	}
}

func (d householdDeps) fill(ctx context.Context, x *agent.DeviceInfo) {
	if v, err := d.settings(ctx, x.ID); err == nil {
		x.Room, x.RemoteTargets = v.HAArea, v.RemoteTargets
	}
	x.Online = d.online(x.ID)
	if !x.Online {
		return
	}
	if det, err := d.detail(ctx, x.ID); err == nil {
		for _, c := range det.Capabilities {
			x.Caps = append(x.Caps, c.ID)
			if c.ID == capschema.DisplayScreenID {
				if sc, ok := screens.ParseScreen(c.State); ok {
					x.Kind = agent.KindScreen
					x.Watch.Off, x.Watch.DrawerOpen = sc.Off, sc.DrawerOpen
				}
			}
		}
	}
	if x.Kind == agent.KindScreen && d.seen != nil {
		x.Watch.LastInput = d.seen(x.ID)
	}
	if np, ok := d.playing(x.ID); ok {
		x.Watch.Playing = np.State == "playing"
		x.Playing = np.Title
		if np.Artist != "" {
			x.Playing += " — " + np.Artist
		}
		if np.State != "" && np.State != "playing" {
			x.Playing += " (" + np.State + ")"
		}
	}
}
