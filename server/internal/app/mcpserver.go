package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/assistant"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/favorites"
	"git.shino.boo/devilreef/cadence/server/internal/history"
	"git.shino.boo/devilreef/cadence/server/internal/mcpserver"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/streams"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
	"git.shino.boo/devilreef/cadence/server/internal/voice"
)

// mcpDeps are what Build hands the MCP endpoint; kept apart so the assembly
// names its seams once.
type mcpsrvDeps struct {
	version   string
	toolbox   *agent.Registry
	target    tools.TargetFunc
	settings  func(ctx context.Context, deviceID string) (settings.Values, error)
	enrolled  *enroll.Service
	online    func(deviceID string) bool
	caps      *devices.Service
	vitals    func(deviceID string) (telemetry.Vitals, bool)
	cmds      *commands.Service
	sessions  *media.Store
	songs     *music.Service
	history   *history.Service
	favorites *favorites.Service
	voices    *voice.Coordinator
	ask       *assistant.Service
	musicKit  *tools.MusicKit
	music     func(ctx context.Context, name string) (providers.Music, error)
	player    func(ctx context.Context) (providers.Player, error)
	stream    func(deviceID string, upstream providers.Stream) (string, error)
	catalog   *catalog.Service
	log       *slog.Logger
}

// buildMCP assembles the MCP endpoint over the same seams the admin api and
// the integrations use.
func buildMCP(d mcpsrvDeps) *mcpserver.Server {
	return mcpserver.New(mcpserver.Deps{
		Version: d.version, Log: d.log,
		Tools:    d.toolbox.Static,
		Target:   d.target,
		Settings: d.settings,
		List:     d.enrolled.List,
		Online:   d.online,
		Detail:   d.caps.Detail,
		Vitals:   d.vitals,
		Send: func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error) {
			return d.cmds.Send(ctx, deviceID, capability, name, args)
		},
		Session: d.sessions.Get,
		Queue:   d.songs.State,
		History: d.history.List,
		Favorite: func(ctx context.Context, query string, limit int) ([]favorites.Entry, error) {
			return d.favorites.List(ctx, query, limit)
		},
		Announce: func(ctx context.Context, deviceID string, a voice.Announcement) (string, error) {
			return d.voices.Announce(ctx, deviceID, a)
		},
		Ask:    d.ask.Ask,
		Track:  d.musicKit.Track,
		Music:  d.music,
		Player: d.player,
		Stream: d.stream,
		RelayExpiry: func() time.Time {
			return time.Now().Add(streams.DefaultTTL)
		},
		ResolveCatalog: func(ctx context.Context, ref, episode string) (catalog.PlayableView, error) {
			return d.catalog.Resolve(ctx, catalog.Household,
				catalog.PlayRequest{Ref: catalog.Ref(ref), Episode: episode, From: catalog.FromStart})
		},
	})
}
