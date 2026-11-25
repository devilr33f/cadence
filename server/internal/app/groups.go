package app

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// groupDevices lists the enrolled devices as the groups' plan reads them
func groupDevices(list func(ctx context.Context) ([]enroll.Listed, error)) func(ctx context.Context) ([]groups.Device, error) {
	return func(ctx context.Context) ([]groups.Device, error) {
		rows, err := list(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]groups.Device, 0, len(rows))
		for _, r := range rows {
			if r.Status == "enrolled" {
				out = append(out, groups.Device{ID: r.ID, Name: r.Name, Model: r.Model, Room: r.Room})
			}
		}
		return out, nil
	}
}

// groupNames are the groups as the agent's target resolver matches them
func groupNames(g *groups.Service) func(ctx context.Context) ([]agent.GroupName, error) {
	return func(ctx context.Context) ([]agent.GroupName, error) {
		named, err := g.Named(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]agent.GroupName, len(named))
		for i, n := range named {
			out[i] = agent.GroupName{Name: n.Name, LeaderID: n.LeaderID}
		}
		return out, nil
	}
}

// groupedMusic is the music queue as the tools reach it: a member of an active group plays, and keeps its queue, on
// its group's leader, which streams to every member. The queue then lives with the leader, so a member's later skip,
// which the commands interceptor also sends to the leader, finds it.
type groupedMusic struct {
	*music.Service
	route func(deviceID, capability string) (string, bool)
}

func (m groupedMusic) to(deviceID string) string {
	if leader, ok := m.route(deviceID, "media.url"); ok {
		return leader
	}
	return deviceID
}

func (m groupedMusic) Play(ctx context.Context, deviceID string, tracks []providers.Track, start int, o music.Options) error {
	return m.Service.Play(ctx, m.to(deviceID), tracks, start, o)
}

func (m groupedMusic) Add(ctx context.Context, deviceID string, tracks []providers.Track, next bool) (int, error) {
	return m.Service.Add(ctx, m.to(deviceID), tracks, next)
}

func (m groupedMusic) StartRadio(ctx context.Context, deviceID string, seed providers.Track) (int, error) {
	return m.Service.StartRadio(ctx, m.to(deviceID), seed)
}

func (m groupedMusic) RadioFromCurrent(ctx context.Context, deviceID string) (int, error) {
	return m.Service.RadioFromCurrent(ctx, m.to(deviceID))
}

func (m groupedMusic) CurrentTrack(deviceID string) (providers.Track, bool) {
	return m.Service.CurrentTrack(m.to(deviceID))
}

func (m groupedMusic) SetShuffle(deviceID string, on bool) { m.Service.SetShuffle(m.to(deviceID), on) }

func (m groupedMusic) SetRepeat(deviceID string, r music.Repeat) {
	m.Service.SetRepeat(m.to(deviceID), r)
}

func (m groupedMusic) Restart(ctx context.Context, deviceID string) error {
	return m.Service.Restart(ctx, m.to(deviceID))
}
