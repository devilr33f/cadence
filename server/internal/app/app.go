// Package app is the composition root: every service is built here and only here.
package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/alarmview"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/assistant"
	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogscreen"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/config"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/deviceops"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/favorites"
	"git.shino.boo/devilreef/cadence/server/internal/follow"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
	"git.shino.boo/devilreef/cadence/server/internal/history"
	"git.shino.boo/devilreef/cadence/server/internal/images"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/hermes"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrplayer"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrserver"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/memory"
	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
	"git.shino.boo/devilreef/cadence/server/internal/presence"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/deepgram"
	"git.shino.boo/devilreef/cadence/server/internal/providers/exa"
	_ "git.shino.boo/devilreef/cadence/server/internal/providers/fake" // registers the dev fakes
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/metno"
	"git.shino.boo/devilreef/cadence/server/internal/providers/openaicompat"
	"git.shino.boo/devilreef/cadence/server/internal/providers/openmeteo"
	"git.shino.boo/devilreef/cadence/server/internal/providers/openrouter"
	"git.shino.boo/devilreef/cadence/server/internal/providers/otlphttp"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ryd"
	"git.shino.boo/devilreef/cadence/server/internal/providers/soundcloud"
	"git.shino.boo/devilreef/cadence/server/internal/providers/spotify"
	"git.shino.boo/devilreef/cadence/server/internal/providers/uniproxy"
	"git.shino.boo/devilreef/cadence/server/internal/providers/vkmusic"
	"git.shino.boo/devilreef/cadence/server/internal/providers/xweb"
	"git.shino.boo/devilreef/cadence/server/internal/providers/yandexmusic"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytmusic"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytweb"
	"git.shino.boo/devilreef/cadence/server/internal/providers/zvuk"
	"git.shino.boo/devilreef/cadence/server/internal/ringsounds"
	"git.shino.boo/devilreef/cadence/server/internal/screenplay"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/skills"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/streams"
	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
	"git.shino.boo/devilreef/cadence/server/internal/titleinfo"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
	"git.shino.boo/devilreef/cadence/server/internal/video"
	"git.shino.boo/devilreef/cadence/server/internal/voice"
	"git.shino.boo/devilreef/cadence/server/internal/wakewords"
)

// Registry is every provider module the server builds instances of, the fakes too with cfg.DevFakes.
func Registry(cfg config.Config, log *slog.Logger) *providers.Registry {
	return providers.NewRegistry(cfg.DevFakes, deepgram.FluxModule, deepgram.AuraModule, uniproxy.STTModule, uniproxy.TTSModule, openrouter.Module, openmeteo.Module, metno.Module, exa.Module, openrouter.EmbedModule, openaicompat.EmbedModule, openaicompat.LLMModule, hermes.Module, ha.Module, mcp.Module, otlphttp.Module,
		ytmusic.Module, spotify.Module, soundcloud.Module, yandexmusic.Module, zvuk.Module, vkmusic.Module, ytdlp.Module(cfg.YTDLP, log), xweb.Module(log), t3code.Module,
		torrplayer.Module, torrserver.Module)
}

// Build returns the app and a cleanup to run after shutdown. tee, the handler
// of log, stores the server's own lines with the devices' once attached; nil
// stores none.
func Build(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger, tee *telemetry.Tee, version string) (*fiber.App, func(), error) {
	box, err := secrets.Load(cfg.MasterKeyFile)
	if err != nil {
		return nil, nil, err
	}
	instances := providers.NewInstances(pool, box, Registry(cfg, log))
	prefs := settings.NewService(pool)
	tokens := auth.NewTokens(db.New(pool), time.Now)
	adminSessions := auth.NewSessions(pool, time.Now)
	login := auth.NewLogin(auth.NewOwner(pool, time.Now), adminSessions, log, time.Now)
	if cfg.CookieInsecure {
		log.Warn("cookie_insecure")
	}
	enrolled := enroll.NewService(db.New(pool), time.Now)
	bus := events.NewBus(0)
	enrolled.PublishTo(bus)
	claims := enroll.NewClaims(pool, time.Now, log)
	claims.PublishTo(bus)
	feed := events.NewFeed(bus, log)
	hub := gateway.NewHub()
	caps := devices.NewService(pool, hub, box, log)
	caps.PublishTo(bus)
	traces := buildTracing(version, log)
	telem := buildTelemetry(telemetryDeps{
		pool: pool, authDevice: enrolled.AuthDevice, caps: caps, global: prefs.EffectiveGlobal, tee: tee, bus: bus,
		tailing: feed.LogsSubscribed, otlp: otlpClient(instances), traces: traces, log: log,
	})
	prefs.OnGlobalChange(telem.maintenance.Poke)
	caps.OnChange(telem.exporter.DevicesChanged)
	cmds := commands.New(caps, log)
	multiroom := groups.New(groups.Deps{
		Store: pool, Box: box, Devices: caps, List: groupDevices(enrolled.List), Online: hub.Online, Bus: bus, Log: log,
		Commands: cmds,
	})
	// a delete (its membership gone) and a rename recompute; a member's changed status reaches the feed
	caps.OnChange(multiroom.Poke)
	caps.OnState(multiroom.StateChanged)
	caps.ShowGroups(multiroom.Refs)
	enrolled.ShowGroups(multiroom.Refs)
	sessions := media.NewStore(bus, log)
	sessions.ShareGroups(func(id string) (string, bool) { return multiroom.Route(id, "media.player") }, multiroom.Followers)
	playHistory := history.New(db.New(pool), log, time.Now)
	sessions.OnSession(playHistory.Observe)
	artwork := media.NewArtwork()
	convs := conversation.New(db.New(pool))
	agentModes := agent.NewModes(pool, log)
	modeState := agent.NewModeState()
	taskManager := tasks.NewManager(tasks.ManagerDeps{Store: tasks.NewStore(pool), Bus: bus, Log: log})
	hermesTasks, hermesInstance := hermesKind(ctx, prefs.Global, hermesClient(instances), pool, log)
	if hermesTasks != nil {
		taskManager.Register(hermesTasks)
	}
	taskAdmin := tasks.NewAdmin(tasks.AdminDeps{Manager: taskManager, Kinds: taskKinds(globalName(prefs.Global, "hermes"), hermesInstance), Log: log})
	haConn, haInstance := homeAssistant(ctx, prefs.Global, haClient(instances), log)
	home := watchHome(haConn, log)
	homeUsage := ha.NewUsage(db.New(pool), log)
	busy := presence.New(presence.Deps{Queries: db.New(pool), Bus: bus, Log: log})
	haAdmin := ha.NewAdmin(ha.AdminDeps{
		Home: home, Usage: homeUsage, Configured: globalName(prefs.Global, "homeAssistant"), Instance: haInstance, Log: log,
	})
	mcpCheck := mcpInstanceCheck(instances.Ref)
	prefs.CheckMCP(mcpCheck)
	mcpStore := mcp.NewStore(db.New(pool))
	mcpCatalog := mcp.NewCatalog(mcp.CatalogConfig{Store: mcpStore, Log: log, Bus: bus})
	mcpLeftOut := newMCPDisabled()
	memories := memory.New(pool)
	recaller := memory.NewRecaller(memory.RecallerDeps{Store: memories, Embeds: instances, GlobalEmbed: globalEmbedName(prefs.Global), Log: log})
	memoryEmbedder := globalEmbedder(globalEmbedName(prefs.Global), instances.Embed)
	memoryAdmin := memory.NewAdmin(memory.AdminDeps{Store: memories, Embedder: memoryEmbedder, Bus: bus, Log: log})
	reembed := memory.NewReembedder(memory.ReembedDeps{Store: memories, Log: log, Embedder: memoryEmbedder})
	modeOf := compactionMode(agentModes)
	learner := memory.NewLearner(memory.LearnerDeps{
		Store: memories, Settings: prefs.Effective, Global: prefs.EffectiveGlobal, Embedder: memoryEmbedder, Window: agent.ContextWindow,
		Mode: modeOf, Bus: bus, Log: log,
	})
	described, pokeModes, stopModes := watchModes(ctx, agentModes, globalAllowedModes(prefs), log)
	agentModes.OnChange(pokeModes)
	prefs.OnGlobalChange(pokeModes)
	taskToolset, taskBoard := taskTools(taskManager, prefs.Effective)
	aerial := aerialFetcher(cfg, log)
	backdrops := &lateArtwork{}
	// the resetter is built with the compactor below, before any link serves
	var resetter *conversation.Resetter
	// the voice coordinator, set once it is built below, before the link serves
	speaker := &announcer{}
	scr := buildScreens(screenDeps{
		hub: hub, caps: caps.State, desired: caps.Desired, settings: prefs.Effective, putSettings: prefs.PutDevice,
		setConfig: caps.SetConfig, enrolled: enrolled, sessions: sessions, convs: convs,
		home: home, usage: homeUsage, aerial: aerial, artwork: backdrops, stop: speaker.Stop,
		weather: instances.Weather, publish: bus.Publish, log: log,
		reset: func(ctx context.Context, deviceID string) error {
			_, err := resetter.Reset(ctx, deviceID, nil)
			return err
		},
	})
	taskScreens := scr.taskScreens(taskManager, cmds, prefs.Effective, log)
	dir := deviceRooms{list: enrolled.List, room: enrolled.Room}
	push := (&screenPush{
		send: hub.Send, isScreen: isScreen(caps.State), summary: taskScreens.Summary,
		screens: linkedDevices(dir.Devices, hub.Online), info: deviceInfo(dir),
		effective: prefs.Effective, desired: caps.Desired, setConfig: caps.SetConfig, log: log,
	}).init()
	taskScreens.OnSummaryChange(push.SummaryChanged)
	alarmPush := (&alarmsPush{send: hub.Send, isScreen: isScreen(caps.State), screens: linkedDevices(dir.Devices, hub.Online),
		log: log}).init()
	alarmWatch := alarmview.NewWatcher(alarmview.Deps{Publish: bus.Publish, Changed: alarmPush.Changed, Log: log})
	alarmPush.summary = alarmview.NewSummaries(alarmWatch, alarmview.SummaryDeps{
		Devices: alarmDevices(enrolled.List), State: caps.State, Online: hub.Online, Desired: caps.Desired,
	}).Summary
	caps.OnState(alarmWatch.StateChanged)
	scr.router.Register(rpc.AlarmsAct, alarmview.ActMethod(alarmview.ActDeps{Command: cmds.Send, Log: log}))
	prefs.OnDeviceChange(push.LanguageChanged)
	prefs.OnGlobalChange(push.AllLanguagesChanged)
	// the household is set once the music queue is built, before any turn
	targets := &agent.Resolver{}
	// an alarm's music and a reminder's speech, kept here and streamed at ring time (P3); ffmpeg makes the music
	ringSounds := ringsounds.New(ringsounds.Deps{
		Q: db.New(pool), Voice: ringVoice(prefs.Effective, instances.TTS), Transcode: ringsounds.FFmpeg(os.Getenv), Log: log,
	})
	// the music tools are built below; an alarm's music reads their results
	var makeAlarmMusic tools.AlarmMusic
	alarmDeps := tools.AlarmDeps{State: caps.State, Command: cmds.Send, Now: time.Now, Log: log, Target: targets.ResolveTarget,
		Sounds: ringSounds, Music: func(ctx context.Context, deviceID, sound string) (ringsounds.Sound, error) {
			if makeAlarmMusic == nil {
				return ringsounds.Sound{}, ringsounds.ErrNoTranscoder
			}
			return makeAlarmMusic(ctx, deviceID, sound)
		}}
	builtin := append([]agent.Tool{
		tools.Weather(tools.WeatherDeps{Settings: prefs.Effective, Weather: instances.WeatherWithFallback}),
		tools.WebSearch(tools.SearchDeps{Settings: prefs.Effective, Search: instances.Search, Found: scr.webFound}),
		tools.XSearch(tools.XSearchDeps{Settings: prefs.Effective, XSearch: instances.XSearch, Found: scr.xFound}),
		tools.AgentMode(tools.ModeDeps{Settings: prefs.Effective, State: modeState, Modes: agentModes, Described: described.Modes, Now: time.Now}),
		agent.ConversationResetTool(),
		tools.DeviceVolume(tools.VolumeDeps{State: caps.State, Command: cmds.Send, Target: targets.ResolveTarget}),
		tools.Bluetooth(tools.BluetoothDeps{Command: cmds.Send, Settings: prefs.Effective, Target: targets.ResolveTarget}),
		tools.Alarm(alarmDeps),
		tools.Timer(alarmDeps),
		tools.Reminder(alarmDeps),
		tools.DoNotDisturb(tools.DNDDeps{Busy: busy, Settings: prefs.Effective}),
	}, homeTools(home, prefs.Effective, homeUsage, scr.confirmations)...)
	relay := streams.New(streams.Config{Log: log, Hosts: musicMediaHosts()})
	imageProxy := images.New(images.Config{Public: artwork.Image})
	artwork.Own = ownImages(cfg.PublicOrigin, imageProxy)
	songs := music.New(music.Deps{
		Settings: prefs.Effective, Player: instances.Player, YouTube: ytmusic.New(ytmusic.Config{Log: log}, httpx.Client(nil)),
		Music: instances.Music, Stream: musicStream(cfg.PublicOrigin, relay), Image: proxiedImage(cfg.PublicOrigin, imageProxy),
		Command: cmds.Deliver, Session: sessions.Raw, Log: log, Now: time.Now,
	})
	songs.RegisterScreen(scr.router)
	sessions.Decorate(songs.Decorate)
	videos := video.New(video.Deps{
		Settings: prefs.Effective, Player: instances.Player, State: caps.State,
		Reports: func(ctx context.Context, dev, c, n string) error {
			_, _, err := caps.Command(ctx, dev, c, n)
			return err
		},
		Relay: relay, Origin: cfg.PublicOrigin, Command: cmds.Deliver, Session: sessions.Raw, Log: log, Now: time.Now,
		Image: proxiedImage(cfg.PublicOrigin, imageProxy),
	})
	videos.RegisterScreen(scr.router)
	songs.UseScreens(screenSongs(videos), songsOnScreen(videos))
	// a group member's media commands go to its leader first, through the whole chain again
	cmds.Intercept(video.Chain(reminderIntercept(ringSounds, cmds.Deliver), multiroom.Intercept(cmds.Send), songs.Intercept,
		videos.Intercept))
	grouped := groupedMusic{Service: songs, route: multiroom.Route}
	prefs.CheckList("catalogSources", catalogInstanceCheck(instances.Ref))
	// a web search's thumbnails reach its link cards through the proxy alone
	scr.images.ref = imageProxy.Ref
	// youtube itself, keyless: search with its filters, channels, playlists and a video's counts
	ytWeb := ytweb.New(ytweb.Config{Log: log}, httpx.Client(nil))
	catalogSvc := catalog.New(catalog.Deps{
		Providers: catalogSources(catalogSourceDeps{global: prefs.EffectiveGlobal, instances: instances,
			relay: catalogRelay(cfg.PublicOrigin, relay), web: ytWeb, log: log}),
		Images: imageProxy, Choices: catalog.NewDBChoices(db.New(pool)), Profile: videos.Profile,
		ImageURL: imageURL(cfg.PublicOrigin), Log: log,
		// Shikimori's and MyAnimeList's public facts, anonymously; the owner's list comes through torrplayer
		Info: titleinfo.New(titleinfo.Config{UserAgent: "Cadence/" + version, Log: log}, httpx.Client(nil)),
	})
	catalogscreen.Register(scr.router, catalogscreen.Deps{Catalog: catalogSvc, Cards: scr.cards, Log: log})
	backdrops.set(catalogSvc.Backdrops())
	playSignal := newSessionSignal()
	playHooks := mediaPlayHooks(scr, sessions, playHistory, playSignal, imageProxy, tools.PlayOutcomeRecorder(convs.Append, log))
	favoriteSongs := favorites.New(db.New(pool), instances.Music, time.Now)
	playHooks.Collection = func(ctx context.Context, info playprogress.CardInfo) {
		videos.PrepareCollection(ctx, info.DeviceID, info.Kind, info.Title, info.Artist, info.ArtworkURL)
	}
	musicKit := tools.NewMusic(tools.MusicDeps{
		Settings: prefs.Effective, Music: instances.Music, Player: grouped, Feedback: favoriteSongs,
		Now: time.Now, Target: targets.ResolveTarget, History: searchHistory(convs), Pending: playHooks,
	})
	builtin = append(builtin, musicKit.Tools()...)
	makeAlarmMusic = alarmMusic(ringSounds, musicKit, songs.Upstream)
	playing := nowPlaying(sessions, songs, time.Now)
	targets.Household = household(householdDeps{
		list: enrolled.List, detail: caps.Detail, settings: prefs.Effective, online: hub.Online, playing: playing,
		seen: scr.rooms.Contexts().Seen,
	})
	targets.Pressed = scr.rooms.PressedBy
	targets.Groups = groupNames(multiroom)
	videoKit := tools.NewVideo(tools.VideoDeps{Settings: prefs.Effective, Player: instances.Player, Videos: videos, Now: time.Now,
		Target: targets.ResolveTarget, History: searchHistory(convs), Pending: playHooks})
	builtin = append(builtin, videoKit.Tools()...)
	// a video tile's press plays it on the screen that pressed
	scr.tools.Videos = videoCards{kit: videoKit, videos: videos}
	builtin = append(builtin, tools.MediaControl(tools.MediaDeps{Command: cmds.Send, Queue: grouped, Target: targets.ResolveTarget}))
	agentCat := agentCatalog{s: catalogSvc}
	// a play's card follows the queue's session: its buffering, then the next song
	scr.tools.Music = musicCards{kit: musicKit, queue: songs, session: sessions.Raw, signal: playSignal}
	screenTools := tools.ScreenDeps{
		History: searchHistory(convs),
		Target:  targets.ResolveTarget, Contexts: scr.rooms.Contexts().Get, Command: cmds.Send, Profile: videos.Profile,
		Catalog: agentCat, ImageURL: imageURL(cfg.PublicOrigin), Voiceovers: catalogVoiceovers(prefs.Effective),
		Found:   scr.catalogFound(openTitle(cmds.Send)),
		Fetch:   playCards{parts: scr, cat: agentCat, session: sessions.Raw, playHistory: playHistory, signal: playSignal, now: time.Now}.fetch,
		Started: playCards{parts: scr, cat: agentCat, session: sessions.Raw, playHistory: playHistory, signal: playSignal, now: time.Now}.started,
	}
	// a device's server fields go to its multiroom followers too (P4), under their own sessions
	var followers *follow.Fanout
	fieldsChanged := fieldsAndFollowers(push.FieldsChanged, func() *follow.Fanout { return followers })
	tracker := screenplay.New(screenplay.Deps{Catalog: agentCat, Session: sessions.Raw, Changed: fieldsChanged, Log: log})
	// the youtube tools' tiles: OK on one plays it on the screen that pressed (the screen's own media.play)
	screenTools.VideoFound = scr.youtubeFound(screenTools.PlayMethod())
	screenTools.Played = func(dev string, p tools.PlayedVideo) {
		a := history.Attribution{ItemID: string(p.Ref)}
		if sess, ok := sessions.Raw(dev); ok && sess.SessionID == p.SessionID {
			playHistory.AttributeObserved(dev, p.SessionID, sess, a)
		} else {
			playHistory.Attribute(dev, p.SessionID, a)
		}
		tracker.Played(dev, screenplay.Played{SessionID: p.SessionID, Ref: p.Ref, TitleRef: p.TitleRef,
			Episode: p.Episode, Choices: p.Choices})
	}
	screenTools.Current, screenTools.Replaced = currentSession(sessions, time.Now), tracker.Forget
	tracker.UsePlay(screenTools.Play)
	songs.OnFieldsChange(fieldsChanged)
	// a video on a group's leading screen opens on its other screens (multiroom P4)
	followers = newFollow(followDeps{groups: multiroom, online: hub.Online, screen: func(ctx context.Context, id string) bool {
		return videos.Profile(ctx, id).Screen
	}, tracker: tracker, sessions: sessions, state: caps.State, screens: screenTools, videos: videos, send: cmds.Send,
		changed: push.FieldsChanged, log: log})
	// a follower's fields are its leader's, ahead of any its own earlier play left
	push.fields = append(push.fields, followFields(followers, songs.SessionFields, tracker.Fields), songs.SessionFields, tracker.Fields)
	toLeader := followRoute(followers, sessions)
	sessions.OnChange(eachDevice(songs.SessionChanged, tracker.SessionChanged, fieldsChanged, playSignal.Changed,
		func(string) { followers.Poke() }))
	caps.OnState(followStates(followers))
	screenTools.Votes = youtubeVotes(prefs.EffectiveGlobal, ryd.New(ryd.Config{Log: log}, httpx.Client(nil)))
	builtin = append(builtin, tools.Screens(screenTools)...)
	builtin = append(builtin, tools.YouTube(screenTools)...)
	scr.router.Register(rpc.MediaPlay, screenTools.PlayMethod())
	mediaMethods{music: songs, catalog: tracker, follow: toLeader, log: log}.register(scr.router.Register)
	remotes := buildRemote(remoteDeps{
		enrolled: enrolled, groups: multiroom, sessions: sessions, caps: caps, cmds: cmds, images: imageProxy,
		artwork: artwork, origin: cfg.PublicOrigin, parts: scr, log: log,
	})
	scr.router.Register(rpc.MediaPanel, catalogscreen.SessionPanel(catalogscreen.Deps{Catalog: catalogSvc, Cards: scr.cards, Log: log}, followRef(tracker.Ref, toLeader)))
	subs := newSubscriptionHub(subscriptionDeps{
		pool: pool, settings: prefs.Effective, convs: convs, online: hub.Online, bus: bus, log: log, awake: cfg.DevAwake,
		busy: busy,
	})
	builtin = append(builtin, subs.tools(prefs.Effective)...)
	t3src := t3code.NewSource(t3code.SourceDeps{
		Followed: func(ctx context.Context, inst string) ([]subscriptions.Topic, error) {
			return subs.hub.Followed(ctx, t3code.ModuleName, inst)
		},
		Log: log,
	})
	t3src.OnChange(subs.hub.Refresh)
	prefs.CheckList("t3code", t3InstanceCheck(instances.Ref))
	t3LeftOut := newMCPDisabled()
	builtin = append(builtin, tools.T3Code(tools.T3Deps{Source: t3src})...)
	toolbox := agent.NewRegistry(append(builtin, taskToolset...)...)
	toolbox.AddSource(mcp.SourceName, mcp.NewSource(mcpCatalog, log))
	mcpAdmin := mcp.NewAdmin(mcp.AdminDeps{
		Catalog: mcpCatalog, Listed: mcpListed(prefs.EffectiveGlobal), IsMCP: mcpIsInstance(mcpCheck),
		Disabled: mcpLeftOut.Code, Skips: toolbox.Skips, Log: log,
	})
	compactor := memory.NewCompactor(memory.CompactorDeps{
		Store: memories, Messages: convs, Settings: prefs.Effective, LLMs: instances, Window: agent.ContextWindow,
		Redact: toolbox.RedactHistory, Hook: learner.Learn, Mode: modeOf, Bus: bus, Log: log, Unit: cfg.DevCompactUnit,
	})
	resetter = conversation.NewResetter(convs, compactor)
	resetter.OnReset(scr.rooms.ConversationReset)
	skillLib := skills.New(skills.Deps{Store: skills.NewDBStore(pool), Builtins: skills.Builtins(), Embeds: instances, Log: log})
	// a failed read leaves the built-ins alone until the next skill change
	_ = skillLib.Load(ctx)
	runner := agent.NewRunner(agent.RunnerDeps{
		Providers: instances, Settings: prefs.Effective, Modes: agentModes,
		ModeState: modeState, Tools: toolbox, Conversations: convs, Memories: recaller, Summaries: compactor, Tasks: taskBoard,
		ResetConversation: func(ctx context.Context, deviceID string) error {
			_, err := resetter.Reset(ctx, deviceID, nil)
			return err
		},
		Inbox: subs.hub.Inbox, Home: homeContext(home, homeUsage), Playing: playing,
		Household: targets.Household, Screens: scr.rooms.Contexts().Get, Catalog: agentCat, Skills: skillLib, Bus: bus, State: caps.State, Now: time.Now, Log: log,
	})
	reports := tasks.NewReporter(tasks.ReporterDeps{
		Manager: taskManager, Turns: runner, Voice: speaker, Online: hub.Online, Settings: prefs.Effective, Log: log,
		Awake: cfg.DevAwake, Busy: busyNow(busy),
	})
	busy.OnChange(reports.BusyChanged)
	voices := voice.New(voice.Deps{
		Settings: prefs.Effective, Providers: instances, Agent: runner, RecordFailure: convs.RecordFailure,
		Mode: runner.PeekMode, EndSession: modeState.EndSession,
		SessionEnded: sessionEnded(reports.SessionEnded, subs.hub.SessionEnded), AnnounceEnded: eachDevice(reports.AnnounceEnded, subs.hub.AnnounceEnded),
		NewEncoder: audio.NewEncoder, Output: voice.OutputFrom(caps.State), Now: time.Now, Log: log,
		MediaPlaying: mediaPlaying(sessions), PauseMedia: pauseMedia(cmds), Transcript: scr.rooms.Transcript,
		Command: cmds.Send,
		HomeControl: func(ctx context.Context, call voice.HomeCall) (string, bool) {
			// the same resolution, guards and policy ha_control runs under, so a
			// direct command and the model cannot do different things
			d := tools.HADeps{Home: home, Settings: prefs.Effective, Policy: tools.HASensitivePolicy(prefs.Effective),
				Usage: homeUsage, Confirmations: scr.confirmations}
			// the spoken light word means the speaker's room, named by its own room
			// when the setting does not name an area
			room := ""
			if detail, err := caps.Detail(ctx, call.DeviceID); err == nil {
				room = detail.Room
			}
			name, reason := tools.DirectHomeControl(ctx, d, agent.Call{
				DeviceID: call.DeviceID, SessionID: call.SessionID, TurnID: call.TurnID, Trigger: call.Trigger,
			}, call.On, call.Target, room)
			if reason != "" {
				log.Debug("direct_home_refused", "device_id", call.DeviceID, "session_id", call.SessionID, "reason", reason)
				return "", false
			}
			return name, true
		},
		StoreCommand: func(ctx context.Context, deviceID, turnID, text, reply, trigger string) error {
			msgs := []providers.Message{{Role: "user", Content: text}}
			if reply != "" {
				msgs = append(msgs, providers.Message{Role: "assistant", Content: reply})
			}
			return convs.Append(ctx, deviceID, turnID, conversation.TurnMeta{Trigger: trigger}, msgs)
		},
		ResetConversation: func(ctx context.Context, deviceID, sessionID, text, reply string) error {
			_, err := resetter.Reset(ctx, deviceID, &conversation.ResetTurn{ID: sessionID, Text: text, Reply: reply})
			return err
		},
	})
	speaker.c = voices
	typed := assistant.New(ctx, assistant.Deps{Run: runner.RunTyped, Announce: announceCode(voices), Bus: bus, Log: log})
	subs.wire(runner, voices)
	zones := newAlarmZones(zoneDeps{Global: prefs.Global, Device: prefs.Device, Config: caps.Desired, SetConfig: caps.SetConfig,
		ConfigKeys: caps.ConfigKeys, Log: log, Session: true})
	deviceOps := deviceops.New(deviceops.Deps{
		Q: db.New(pool), Logs: telem.logs, Bus: bus, Link: hub, Box: box,
		Devices:   deviceops.StoreLookup(db.New(pool)),
		AdbServer: adbServerAddr(os.Getenv("CADENCE_ADB_SERVER")), Log: log,
	})
	stopOps := deviceOps.Run(ctx)
	wakeWords := wakewords.New(wakewords.Deps{Q: db.New(pool), Bus: bus, Negatives: cfg.WakeWordNegatives, Log: log})
	stopWakeWords := wakeWords.Run(ctx)
	mcpsrv := buildMCP(mcpsrvDeps{
		version: version, toolbox: toolbox, target: targets.ResolveTarget, settings: prefs.Effective,
		enrolled: enrolled, online: hub.Online, caps: caps, vitals: telem.admin.Vitals, cmds: cmds,
		sessions: sessions, songs: songs, history: playHistory, favorites: favoriteSongs, voices: voices, ask: typed,
		musicKit: musicKit, music: instances.Music,
		player: func(ctx context.Context) (providers.Player, error) {
			v, err := prefs.EffectiveGlobal(ctx)
			if err != nil || v.MusicPlayer == "" {
				return nil, errors.New("no musicPlayer setting")
			}
			return instances.Player(ctx, v.MusicPlayer)
		},
		stream: musicStream(cfg.PublicOrigin, relay), catalog: catalogSvc, log: log,
	})
	link := deviceLink(linkDeps{
		caps: caps, cmds: cmds, sessions: sessions, voices: voices, seen: enrolled.Seen, zones: zones,
		connected: eachDevice(reports.Connected, subs.hub.Connected, push.Linked, alarmPush.Linked), presence: caps, screens: &scr,
		info: push.info, groups: multiroom, ops: deviceOps, version: version, log: log,
	})
	app := api.NewApp(withLogin(api.Options{
		DB: pool, Log: log, Version: version, VerifyToken: tokens.Verify, Tags: adminTags,
		Admin: adminOps(adminDeps{
			enrolled: enrolled, claims: claims, online: hub.Online, caps: caps, cmds: cmds, sessions: sessions, history: playHistory, favorites: favoriteSongs, songs: songs, videos: videos, artwork: artwork, voices: voices, assistant: typed, instances: instances, prefs: prefs, modes: agentModes, convs: convs, resets: resetter,
			memory: memoryAdmin, tasks: taskAdmin, ha: haAdmin, mcp: mcpAdmin, login: login, tokens: tokens, presence: busy,
			telemetry: telem.admin, traces: traces, subscriptions: subs.hub, groups: multiroom, ops: deviceOps, wakeWords: wakeWords, log: log,
			catalog: catalogSvc, catalogSessions: tracker, images: imageProxy, playCatalog: screenTools.PlayMethod(), skills: skillLib, mcpsrv: mcpsrv,
			t3: t3code.AdminDeps{Source: t3src, Listed: t3Listed(prefs.EffectiveGlobal), Disabled: t3LeftOut.Code, Log: log},
		}),
		Plain: adminPlain(feed, telem.prom, deviceOps.UploadRoute()),
		MCP:   mcpsrv.Fiber(),
		Trace: tracing.Middleware(untracedPath),
		Device: []func(fiber.Router){
			func(r fiber.Router) { enroll.MountDevice(r, enrolled) },
			func(r fiber.Router) { enroll.MountClaims(r, claims, cfg.PublicOrigin) },
			func(r fiber.Router) { gateway.Mount(r, hub, enrolled.AuthDevice, link, log) },
			telem.ingest.Mount,
			func(r fiber.Router) { artwork.MountDevice(r, enrolled.AuthDevice, log) },
			mountSessionArtwork(sessionArtworkDeps{
				auth: enrolled.AuthDevice, session: sessions.Get, art: remoteArt(cfg.PublicOrigin, imageProxy, artwork),
				images: imageProxy, log: log,
			}),
			func(r fiber.Router) { ringSounds.Mount(r, enrolled.AuthDevice) },
			func(r fiber.Router) { deviceOps.MountDevice(r, enrolled.AuthDevice) },
			func(r fiber.Router) { wakeWords.MountDevice(r, enrolled.AuthDevice) },
			relay.Mount,
			imageProxy.Mount,
		},
	}, cfg, login))
	stopYandex, err := startYandexProxy(ctx, cfg.YandexProxy, telem.logs, caps.ScrubSecrets, log)
	if err != nil {
		return nil, nil, err
	}
	stopPrune := convs.RunRetention(ctx, log, time.Hour)
	stopHistoryPrune := playHistory.RunRetention(ctx, log, time.Hour)
	stopRingSounds := ringSounds.Run(ctx, ringSoundsGCEvery, ringSoundRefs(enrolled.List, caps.State))
	stopClaims := claims.Run(ctx)
	stopAmbient := runAmbient(ctx, aerial)
	stopReembed := reembed.Run(ctx)
	stopCompact := compactor.Run(ctx)
	stopHome := runHomeAssistant(ctx, home)
	stopHermes := func() {}
	if hermesTasks != nil {
		stopHermes = hermesTasks.Run(ctx)
	}
	stopTasks := taskManager.Run(ctx)
	stopScreens := scr.rooms.Run(ctx, bus)
	stopLeases := scr.leases.Run(ctx)
	stopCards := scr.cards.Run(ctx)
	stopTaskScreens := taskScreens.Run(ctx)
	stopPush := push.Run(ctx)
	stopRenames := push.WatchRenames(bus)
	stopRemotes := remotes.Run(ctx, bus)
	stopAlarmPush := alarmPush.Run(ctx, bus, alarmWatch.Forget)
	pokeMCP, stopMCP := runMCP(ctx, mcpDeps{
		Global: prefs.EffectiveGlobal, Ref: instances.Ref, Build: mcpClient(instances), Servers: mcpCatalog, Log: log,
		Disabled: mcpLeftOut,
	})
	prefs.OnGlobalChange(pokeMCP)
	instances.OnChange(pokeMCP)
	stopMCPPrune := mcpStore.RunRetention(ctx, log, time.Hour)
	stopSessionPrune := adminSessions.RunRetention(ctx, log, 24*time.Hour)
	stopLoginSweep := login.Run(ctx, time.Minute)
	pokeBusy, stopBusy := runPresence(ctx, busy, home, prefs.EffectiveGlobal)
	prefs.OnGlobalChange(pokeBusy)
	stopReports := reports.Run(ctx)
	stopSubscriptions := subs.run(ctx, log)
	pokeT3, stopT3 := runT3Code(ctx, t3Deps{
		Global: prefs.EffectiveGlobal, Ref: instances.Ref, Build: t3Client(instances), Source: t3src,
		Registry: subs.hub.Registry(), Log: log, Disabled: t3LeftOut,
	})
	prefs.OnGlobalChange(pokeT3)
	instances.OnChange(pokeT3)
	stopZones := zones.Run(ctx)
	stopGroups := multiroom.Run(ctx)
	stopFollowers := followers.Run(ctx)
	stopTracing := runTracing(ctx, traces, bus, feed.TracesSubscribed)
	pokeExport, stopTelemetry := telem.run(ctx)
	prefs.OnGlobalChange(pokeExport)
	instances.OnChange(pokeExport)
	// links and feeds end before the pool closes, so no handler outlives it
	return app, func() {
		stopYandex()
		stopOps()
		stopWakeWords()
		stopZones()
		stopFollowers()
		stopGroups()
		stopClaims()
		stopAmbient()
		stopModes()
		stopPrune()
		stopHistoryPrune()
		stopRingSounds()
		stopReembed()
		memoryAdmin.Close()
		stopCompact()
		stopReports()
		stopBusy()
		stopSubscriptions()
		stopT3()
		// calls end before the services they read stop
		scr.router.Close()
		stopTaskScreens()
		stopRenames()
		stopRemotes()
		stopAlarmPush()
		stopPush()
		stopCards()
		stopScreens()
		stopLeases()
		scr.confirmations.Close()
		stopTasks()
		stopHermes()
		stopHome()
		stopMCP()
		stopMCPPrune()
		stopSessionPrune()
		stopLoginSweep()
		songs.StopFeedback()
		feed.Close(websocket.CloseGoingAway, "")
		hub.Close(websocket.CloseGoingAway, "")
		stopTracing()
		// last: the other parts' final lines are stored before the pool closes
		stopTelemetry()
	}, nil
}

// adbServerAddr answers the adb server sidecar's address from CADENCE_ADB_SERVER: the pod-local
// default when unset, none at all when "off" (the adb transport then refuses, link and ssh stay).
func adbServerAddr(v string) string {
	if v == "" {
		return "localhost:5037"
	}
	if v == "off" || v == "none" {
		return ""
	}
	return v
}

// compactionMode reads an agent mode as the maintenance call's model, effort and bounds.
func compactionMode(modes interface {
	Get(ctx context.Context, name string) (agent.Mode, error)
}) memory.ModeFunc {
	return func(ctx context.Context, name string) (memory.ModeLimits, bool, error) {
		m, err := modes.Get(ctx, name)
		if errors.Is(err, agent.ErrModeNotFound) {
			return memory.ModeLimits{}, false, nil
		}
		if err != nil {
			return memory.ModeLimits{}, false, err
		}
		return memory.ModeLimits{
			Model: m.Model, Reasoning: m.Reasoning, MaxTokens: m.MaxTokens,
			Attempt: m.MaxDuration, FirstToken: m.FirstTokenTimeout,
		}, true, nil
	}
}
