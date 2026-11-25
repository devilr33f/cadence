package app

import (
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/assistant"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/config"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/deviceops"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/favorites"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
	"git.shino.boo/devilreef/cadence/server/internal/history"
	"git.shino.boo/devilreef/cadence/server/internal/images"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
	"git.shino.boo/devilreef/cadence/server/internal/mcpserver"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/memory"
	"git.shino.boo/devilreef/cadence/server/internal/modes"
	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/presence"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/skills"
	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
	"git.shino.boo/devilreef/cadence/server/internal/video"
	"git.shino.boo/devilreef/cadence/server/internal/voice"
	"git.shino.boo/devilreef/cadence/server/internal/wakewords"
)

type adminDeps struct {
	wakeWords *wakewords.Service
	enrolled  *enroll.Service
	claims    *enroll.ClaimService
	online    func(deviceID string) bool
	caps      *devices.Service
	cmds      *commands.Service
	sessions  *media.Store
	history   *history.Service
	favorites *favorites.Service
	songs     *music.Service
	videos    *video.Service
	artwork   *media.Artwork
	voices    *voice.Coordinator
	assistant *assistant.Service
	instances *providers.Instances
	prefs     *settings.Service
	modes     *agent.Modes
	convs     *conversation.Store
	resets    *conversation.Resetter
	memory    *memory.Admin
	tasks     *tasks.Admin
	ha        *ha.Admin
	mcp       *mcp.Admin
	login     api.Sessions
	tokens    api.TokenStore
	telemetry telemetry.AdminDeps
	traces    *tracing.Provider
	presence  *presence.Service
	// subscriptions is the subscriptions hub
	subscriptions *subscriptions.Hub
	// t3 is the t3code status route's
	t3 t3code.AdminDeps
	// catalog and images are the TV catalog's and its image proxy's; playCatalog is the screen's own media.play
	// method, served to an ha token's callers by play-catalog
	catalog         *catalog.Service
	catalogSessions catalog.SessionTracker
	images          *images.Proxy
	playCatalog     calls.Method
	skills          *skills.Library
	groups          *groups.Service
	// ops is the device-update service (artifacts, runs, access); its routes are the owner's alone
	ops *deviceops.Service
	// mcpsrv is the MCP endpoint, whose tool list the tokens' editor reads
	mcpsrv *mcpserver.Server
	log    *slog.Logger
}

// adminTags are the document's tags, one per area an operation names.
var adminTags = []*huma.Tag{
	{Name: "enroll", Description: "Create devices with a one-time enrol code, list devices and boards."},
	{Name: "devices", Description: "A device's detail with its capability descriptors, its rename, room and delete, capability config and sealed secrets."},
	{Name: "claims", Description: "Screens that joined device-first: their open claims, each approved with a name and a room, or declined."},
	{Name: "commands", Description: "Send a capability command to a connected device."},
	{Name: "media", Description: "A device's media session and its cover art, the server's music queue for it, and YouTube videos played on it."},
	{Name: "history", Description: "The household's recently played items, kept for 14 days, and its durable music likes, dislikes and favorites."},
	{Name: "voice", Description: "Speak an announcement on a device."},
	{Name: "providers", Description: "Provider instances: STT, TTS, LLM and other modules, with their sealed secrets."},
	{Name: "settings", Description: "Sparse global and per-device settings."},
	{Name: "modes", Description: "Agent modes: each one's model, reasoning, budgets, tools and voice aliases."},
	{Name: "skills", Description: "Agent skills: instruction packs a turn loads when its input calls for them, the built-in ones and the owner's, which may replace or switch off a built-in by its name."},
	{Name: "conversations", Description: "A device's conversation history: its turns, newest first, a page at a time, and each turn's messages as stored; and a turn the owner types for a device's assistant."},
	{Name: "memory", Description: "Long-term memories, their approval queue, compactions and learning runs."},
	{Name: "tasks", Description: "Background tasks, their questions and approvals."},
	{Name: "homeassistant", Description: "The Home Assistant integration: status, entities, areas and usage."},
	{Name: "mcp", Description: "MCP servers and their tools: overrides and acknowledgements."},
	{Name: "mcpserver", Description: "Cadence's own MCP endpoint at /api/v1/mcp: every tool it serves an mcp-scoped token, which a token's own list may narrow."},
	{Name: "session", Description: "The owner's dashboard login: a session cookie, held to csrf rules."},
	{Name: "tokens", Description: "API tokens and their scopes: admin (everything), ha (devices, their media and vitals, commands and announcements, the TV catalog's reads and play), metrics, presence (busy signals)."},
	{Name: "subscriptions", Description: "Subscriptions to outside work (threads, tasks), what can be followed, each device's inbox of updates and answers to their questions."},
	{Name: "telemetry", Description: "Stored log lines, a device's metric series, its top processes and its vitals."},
	{Name: "tracing", Description: "Traces kept in memory: the server's spans and the devices', live and recent, and the trace store's and export's counts."},
	{Name: "t3code", Description: "T3 Code: the instances the t3code setting names, their connection and thread counts."},
	{Name: "catalog", Description: "The TV catalog over torrplayer, TorrServer and YouTube Music: its providers and sources, search, browse, items, their options, actions and panels, and the image proxy's images."},
	{Name: "groups", Description: "Multiroom playback groups and stereo pairs: their members, channels and trims, the leader each one elects, each member's live sync status, and a device's join and leave (Home Assistant's grouping)."},
	{Name: "presence", Description: "Busy mode: signals that the owner is in a call, a meeting, screensharing or not to be disturbed, which hold unasked speech on the devices they cover."},
	{Name: "wakewords", Description: wakewords.TagDescription},
	{Name: "ops", Description: "Device updates: build artifacts per board, and the owner-initiated runs that install them over the device link, adb or ssh, each run's steps and its rollback, and the out-of-band access (adb, ssh) the server-driven installs use."},
}

// AdminSpec is the admin api's OpenAPI document, built with every operation
// registered and no database, for the generator and the drift test.
func AdminSpec(version string) ([]byte, error) {
	return api.Spec(version, adminTags, adminOps(adminDeps{}))
}

// adminOps are the admin api's huma operations, on the /api/v1 group.
func adminOps(d adminDeps) []func(huma.API) {
	return []func(huma.API){
		func(a huma.API) { enroll.RegisterAdmin(a, d.enrolled, d.online) },
		func(a huma.API) { enroll.RegisterClaimsAdmin(a, d.claims) },
		func(a huma.API) { devices.RegisterAdmin(a, d.caps) },
		func(a huma.API) { commands.RegisterAdmin(a, d.cmds) },
		func(a huma.API) { media.RegisterAdmin(a, d.sessions, d.caps, d.artwork) },
		func(a huma.API) { history.RegisterAdmin(a, d.history, d.log) },
		func(a huma.API) { favorites.RegisterAdmin(a, d.favorites) },
		func(a huma.API) { music.RegisterAdmin(a, d.songs, d.caps) },
		func(a huma.API) { video.RegisterAdmin(a, d.videos, d.caps) },
		func(a huma.API) { voice.RegisterAdmin(a, d.voices, d.caps) },
		func(a huma.API) { assistant.RegisterAdmin(a, d.assistant, d.caps) },
		func(a huma.API) { providers.RegisterAdmin(a, d.instances, d.log) },
		func(a huma.API) { settings.RegisterAdmin(a, d.prefs) },
		func(a huma.API) { modes.RegisterAdmin(a, d.modes, d.log) },
		func(a huma.API) { skills.RegisterAdmin(a, d.skills, d.log) },
		func(a huma.API) { conversation.RegisterAdmin(a, d.convs, d.resets, d.log) },
		func(a huma.API) { memory.RegisterAdmin(a, d.memory) },
		func(a huma.API) { tasks.RegisterAdmin(a, d.tasks) },
		func(a huma.API) { ha.RegisterAdmin(a, d.ha) },
		func(a huma.API) { mcp.RegisterAdmin(a, d.mcp) },
		func(a huma.API) { api.RegisterSession(a, d.login) },
		func(a huma.API) { mcpserver.RegisterAdmin(a, d.mcpsrv) },
		func(a huma.API) {
			var known func() []string
			if d.mcpsrv != nil {
				known = d.mcpsrv.Names
			}
			api.RegisterTokens(a, d.tokens, known, d.log)
		},
		func(a huma.API) { telemetry.RegisterAdmin(a, d.telemetry) },
		func(a huma.API) { tracing.RegisterAdmin(a, d.traces) },
		func(a huma.API) { presence.RegisterAdmin(a, d.presence) },
		func(a huma.API) { subscriptions.RegisterAdmin(a, d.subscriptions, d.log) },
		func(a huma.API) { catalog.RegisterAdmin(a, d.catalog) },
		func(a huma.API) { catalog.RegisterPlayAdmin(a, d.playCatalog) },
		func(a huma.API) { catalog.RegisterSessionAdmin(a, d.catalog, d.playCatalog, d.catalogSessions) },
		func(a huma.API) { images.RegisterAdmin(a, d.images) },
		func(a huma.API) { t3code.RegisterAdmin(a, d.t3) },
		func(a huma.API) { groups.RegisterAdmin(a, d.groups) },
		func(a huma.API) { deviceops.RegisterAdmin(a, d.ops) },
		func(a huma.API) { wakewords.RegisterAdmin(a, d.wakeWords) },
	}
}

// adminPlain are the admin routes that are no huma operation, each behind the
// scopes it names: the live feed, the Prometheus exposition and the artifact
// upload (a body of bytes, not a huma body).
func adminPlain(feed *events.Feed, prom *telemetry.Prom, upload api.PlainRoute) []api.PlainRoute {
	return []api.PlainRoute{feed.Route(), prom.Route(), upload}
}

// withLogin puts the owner's login and the bootstrap config's cookie rules on
// the api's options.
func withLogin(o api.Options, cfg config.Config, login api.Sessions) api.Options {
	o.Sessions, o.Origin, o.TrustedProxies, o.CookieInsecure = login, cfg.PublicOrigin, cfg.TrustedProxies, cfg.CookieInsecure
	return o
}
