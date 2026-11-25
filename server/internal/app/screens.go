package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/ambient"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/screens"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
	taskcards "git.shino.boo/devilreef/cadence/server/internal/tasks/cards"
	"git.shino.boo/devilreef/cadence/server/internal/video"
)

// deviceRooms is the rooms directory: each device's room field (W0-C item 9a), set at a claim's approval or in the
// dashboard.
type deviceRooms struct {
	list func(ctx context.Context) ([]enroll.Listed, error)
	room func(ctx context.Context, id string) string
}

func (r deviceRooms) Room(ctx context.Context, id string) string { return r.room(ctx, id) }

// Name is a device's owner-set name, "" for an unknown one
func (r deviceRooms) Name(ctx context.Context, id string) string {
	ds, err := r.list(ctx)
	if err != nil {
		return ""
	}
	for _, d := range ds {
		if d.ID == id {
			return d.Name
		}
	}
	return ""
}

func (r deviceRooms) Devices(ctx context.Context) []string {
	ds, err := r.list(ctx)
	if err != nil {
		return nil
	}
	ids := make([]string, len(ds))
	for i, d := range ds {
		ids[i] = d.ID
	}
	return ids
}

func screenLang(s tools.SettingsFunc) func(context.Context, string) string {
	return func(ctx context.Context, id string) string {
		v, err := s(ctx, id)
		if err != nil {
			return ""
		}
		return v.Language
	}
}

type screenDeps struct {
	hub *gateway.Hub
	// settings.set's writes: the device's setting overrides and a capability's desired config
	putSettings func(ctx context.Context, deviceID string, body []byte) (settings.Overrides, error)
	setConfig   func(ctx context.Context, deviceID, capability string, cfg json.RawMessage) (int64, error)
	caps        screens.StateFunc
	desired     func(ctx context.Context, deviceID, capability string) (json.RawMessage, error)
	aerial      *ambient.Fetcher
	artwork     ambient.Artwork
	settings    tools.SettingsFunc
	enrolled    *enroll.Service
	sessions    *media.Store
	convs       *conversation.Store
	home        *ha.Home
	usage       tools.HAUsage
	// a device's voice session stopped by its room's screen
	stop func(deviceID, sessionID string) bool
	// assistant.reset: move the room conversation's boundary and fan out the chat reset
	reset func(ctx context.Context, deviceID string) error
	// the weather provider instances, for ambient's overlay
	weather func(ctx context.Context, name string) (providers.Weather, error)
	// publish puts each call card's latest state on the live feed
	publish func(events.Event)
	log     *slog.Logger
}

// screenParts are cadence cards, the room fan-out and the rpc router
// (stream N): built before the tools, which attach their cards to turns
type screenParts struct {
	router *calls.Router
	cards  *cards.Service
	rooms  *screens.Service
	// tools builds each tool call's cards on the turn's screens
	tools *cards.ToolCards
	// the image proxy, set once it is built (after the tools take their methods)
	images *imageRefs
	// the screens' leases on live Home Assistant cards
	leases *cards.Leases
	// the Home Assistant commands' late confirmations, shared with the tools
	confirmations *tools.HAConfirmations
}

// imageRefs holds the image proxy's Ref once it exists
type imageRefs struct {
	ref func(rawURL string, local bool) string
}

func buildScreens(d screenDeps) screenParts {
	lang := screenLang(d.settings)
	cs := cards.New(cards.Deps{Lang: lang, Record: cards.Recorder(d.convs.Append, d.log), Log: d.log})
	confirmations := tools.NewHAConfirmations(d.home, tools.HAConfirmationOptions{
		Log: d.log, Record: tools.HAConfirmationRecorder(d.convs.Append, d.log),
	})
	toolCards := &cards.ToolCards{HA: cards.HADeps{
		Home: d.home, Policy: tools.HASensitivePolicy(d.settings), Usage: d.usage, Confirmations: confirmations,
	}}
	imgs := &imageRefs{}
	// covers go through the image proxy once it exists
	toolCards.Images = func(u string) string {
		if imgs.ref == nil {
			return ""
		}
		return imgs.ref(u, false)
	}
	leases := cards.NewLeases(cards.LeaseDeps{Cards: cs, Send: d.hub.Send, Log: d.log})
	dir := deviceRooms{list: d.enrolled.List, room: d.enrolled.Room}
	rooms := screens.New(screens.Deps{
		Live:      leases.Allow,
		Directory: dir, Name: dir.Name, State: d.caps, Online: d.hub.Online,
		Send: d.hub.Send, Playing: mediaPlaying(d.sessions), Lang: lang, Cards: cs, Calls: toolCards, Publish: d.publish,
		StopSession: d.stop, ResetConversation: d.reset, Log: d.log,
	})
	r := calls.New(d.hub.Send, d.log)
	r.Register(rpc.CardAction, cs.ActionMethod())
	r.Register(rpc.CardsWatch, cs.WatchMethod())
	r.Register(rpc.AssistantInvoke, rooms.InvokeMethod())
	r.Register(rpc.AssistantStop, rooms.StopMethod())
	r.Register(rpc.AssistantReset, rooms.ResetMethod())
	screenMethods(r.Register, screenSettings{
		effective: d.settings, put: d.putSettings, desired: d.desired, setConfig: d.setConfig, log: d.log,
	})
	lines := ambient.NewWeatherLines(ambient.WeatherDeps{Place: weatherPlace(d.settings), Weather: d.weather})
	r.Register(rpc.AmbientNext, ambientMethod(ambientService(d.aerial, d.artwork, lines), screenAmbient(d.caps, d.desired, lang)))
	r.Register(rpc.CardsLease, leases.LeaseMethod())
	r.Register(rpc.CardsRelease, leases.ReleaseMethod())
	return screenParts{router: r, cards: cs, rooms: rooms, tools: toolCards, images: imgs, leases: leases, confirmations: confirmations}
}

// musicCards is what the music cards read: the music tools' results, the queue's song, a tile's play
type musicCards struct {
	kit   *tools.MusicKit
	queue *music.Service
	// the device's media session and the wake of every session change, for a play card that follows the queue
	session func(deviceID string) (media.MediaSession, bool)
	signal  *sessionSignal
}

// MusicPlayback is the device's session while it plays a song of the queue: its state and its player's buffering
// (a screen's player says how far it is; a speaker's says nothing)
func (m musicCards) MusicPlayback(deviceID string) (cards.MusicPlayback, bool) {
	if m.session == nil || !m.queue.Owns(deviceID) {
		return cards.MusicPlayback{}, false
	}
	s, ok := m.session(deviceID)
	if !ok {
		return cards.MusicPlayback{}, false
	}
	pb := cards.MusicPlayback{State: s.State, BufferingPercent: -1, BufferedMs: s.BufferedMs}
	if s.Buffering != nil {
		pb.BufferingPercent = int(*s.Buffering)
	}
	return pb, true
}

// MusicChanged waits for any device's session to change
func (m musicCards) MusicChanged(ctx context.Context) bool {
	if m.signal == nil {
		<-ctx.Done()
		return false
	}
	select {
	case <-m.signal.wait():
		return true
	case <-ctx.Done():
		return false
	}
}

func (m musicCards) MusicResult(deviceID, id string) (cards.MusicItem, bool) {
	r, ok := m.kit.Result(deviceID, id)
	return cards.MusicItem{Kind: r.Kind, Title: r.Title, Artist: r.Artist, Owner: r.Owner, Album: r.Album, DurationS: r.DurationS,
		TrackCount: r.TrackCount, ArtworkURL: r.ArtworkURL}, ok
}

func (m musicCards) MusicNow(deviceID string) (cards.MusicItem, bool) {
	v := m.queue.State(deviceID)
	if v.Current == nil {
		return cards.MusicItem{}, false
	}
	c := v.Current
	return cards.MusicItem{Title: c.Title, Artist: c.Artist, Album: c.Album, DurationS: c.DurationS, ArtworkURL: c.ArtworkURL}, true
}

func (m musicCards) MusicPlay(ctx context.Context, deviceID, id string) error {
	return m.kit.Play(ctx, deviceID, id)
}

// screenSongs plays a song on a screen's own player: its YouTube video as music, shown as its audio with the cover
func screenSongs(videos *video.Service) music.ScreenPlay {
	return func(ctx context.Context, dev string, t providers.Track) (string, error) {
		res, err := videos.Play(ctx, dev, video.Request{
			VideoID: t.VideoID, Title: t.Title, Channel: t.Artist, ArtworkURL: t.ArtworkURL, ScreenOnly: true, Music: true,
			View: video.ViewAudio,
		})
		return res.Title, err
	}
}

// songsOnScreen is whether a device plays songs on its screen's own player (mpv), shown on its audio page: every
// screen does, as a screen's audiod has no url source, and older tv apps still report a media.url that answers
// every play unsupported
func songsOnScreen(videos *video.Service) func(ctx context.Context, dev string) bool {
	return func(ctx context.Context, dev string) bool { return videos.Profile(ctx, dev).Screen }
}

// taskScreens registers tasks.list and tasks.item and answers the service
// that sends task toasts
func (p screenParts) taskScreens(m *tasks.Manager, cmds *commands.Service, settings tools.SettingsFunc, log *slog.Logger) *taskcards.Service {
	tc := taskcards.New(taskcards.Deps{
		Manager: m, Cards: p.cards, Screens: p.rooms.RoomScreens, Settings: settings, Log: log,
		Toast: func(ctx context.Context, id string, args []byte) error {
			_, err := cmds.Send(ctx, id, capschema.DisplayScreenID, "toast", args)
			return err
		},
	})
	// before any turn: a task tool's call shows the task's live card
	p.tools.Task = tc.Card
	p.router.Register(rpc.TasksList, tc.ListMethod())
	p.router.Register(rpc.TasksItem, tc.ItemMethod())
	return tc
}

// webFound attaches a web search's link cards to the turn on the room's screens
func (p screenParts) webFound(c agent.Call, s providers.Search, rs []providers.SearchResult) {
	text, _ := s.(providers.PageText)
	var refs cards.ImageRefs
	if p.images != nil && p.images.ref != nil {
		refs = func(u string) string { return p.images.ref(u, false) }
	}
	if subj := cards.WebResultsWithImages(c.TurnID+":"+rand.Text()[:8], rs, text, refs); subj != nil {
		p.rooms.Attach(c.DeviceID, c.SessionID, screens.CallKey(c.TurnID, c.CallID), subj)
	}
}

// xFound attaches an X search's post cards the same way
func (p screenParts) xFound(c agent.Call, posts []providers.Post) {
	if subj := cards.Posts(c.TurnID+":"+rand.Text()[:8], posts); subj != nil {
		p.rooms.Attach(c.DeviceID, c.SessionID, screens.CallKey(c.TurnID, c.CallID), subj)
	}
}

// tapped shows the room fan-out what the voice service sends a device
type tapped struct {
	gateway.Conn
	tap func(deviceID string, env *cadencev2.Envelope)
}

func (c tapped) Send(env *cadencev2.Envelope) error {
	err := c.Conn.Send(env)
	if err == nil {
		c.tap(c.DeviceID(), env)
	}
	return err
}

// searchHistoryMessages bounds the stored messages a lost search id is looked for in
const searchHistoryMessages = 200

// searchHistory is the device's stored conversation for the search tools: an id a follow-up names after a restart
// is found again in the search that answered it (tools.HistoryFunc)
func searchHistory(convs *conversation.Store) tools.HistoryFunc {
	return func(ctx context.Context, dev string) ([]providers.Message, error) {
		stored, err := convs.After(ctx, dev, 0, time.Now(), searchHistoryMessages)
		if err != nil {
			return nil, err
		}
		out := make([]providers.Message, len(stored))
		for i, s := range stored {
			out[i] = s.Message
		}
		return out, nil
	}
}

// videoCards is what the video cards read: the video tools' remembered results, and the
// play a tile's press makes on the screen that pressed (its own player, the play a
// video_play makes there)
type videoCards struct {
	kit    *tools.VideoKit
	videos *video.Service
}

func (v videoCards) VideoResult(deviceID, id string) (cards.VideoFound, bool) {
	r, ok := v.kit.VideoResult(deviceID, id)
	return cards.VideoFound{Title: r.Title, Channel: r.Channel, ArtworkURL: r.ThumbnailURL(), DurationS: r.DurationS,
		Live: r.Live}, ok
}

func (v videoCards) VideoPlay(ctx context.Context, deviceID, id string) error {
	r, ok := v.kit.VideoResult(deviceID, id)
	if !ok {
		return errors.New("the result is out of date")
	}
	_, err := v.videos.Play(ctx, deviceID, video.Request{VideoID: r.ID, Title: r.Title, Channel: r.Channel,
		ArtworkURL: r.ThumbnailURL(), ScreenOnly: true})
	return err
}
