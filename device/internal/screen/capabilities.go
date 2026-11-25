package screen

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"sync"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

var (
	displayConfig    = mustSchema(capschema.DisplayScreenConfig)
	videoConfig      = mustSchema(capschema.MediaVideoConfig)
	mpvCfg           = mustSchema(capschema.MediaMpvConfig)
	mpvState         = mustSchema(capschema.MediaMpvState)
	remoteConfig     = mustSchema(capschema.InputRemoteConfig)
	screencastConfig = mustSchema(capschema.MediaScreencastConfig)
	displayCommands  = compileAll(capschema.DisplayScreenCommands)
	videoCommands    = compileAll(capschema.MediaVideoCommands)
	remoteCommands   = compileAll(capschema.InputRemoteCommands)
)

func compileAll(m map[string]string) map[string]schema {
	out := make(map[string]schema, len(m))
	for name, src := range m {
		out[name] = mustSchema(src)
	}
	return out
}

// applyConfig takes a capability's config the screen applies: checked here, kept for every screen that attaches,
// and sent to the one attached now
func (s *Server) applyConfig(id string, sc schema, cfg json.RawMessage) error {
	if !sc.valid(cfg, nil) {
		return &capability.ApplyError{Code: codeInvalid}
	}
	s.mu.Lock()
	s.configs[id] = string(cfg)
	s.mu.Unlock()
	s.send(&screenv1.Envelope{Body: &screenv1.Envelope_CapabilityConfig{CapabilityConfig: &screenv1.CapabilityConfig{
		Capability: id, Json: string(cfg),
	}}})
	if id == capschema.DisplayScreenID {
		// its language is Ready's
		s.send(s.ready())
	}
	return nil
}

// language is display.screen's config language, the screen's ui language; "" until the server set one. called with
// s.mu held
func (s *Server) language() string {
	var c struct {
		Language string `json:"language"`
	}
	_ = json.Unmarshal([]byte(s.configs[capschema.DisplayScreenID]), &c)
	return c.Language
}

// display is display.screen: the screen's power and page, its size and decoders (the device profile the server picks
// renditions by), and its commands wake, sleep, toast and show
type display struct {
	s     *Server
	state chan json.RawMessage
}

func newDisplay(s *Server) *display {
	d := &display{s: s, state: make(chan json.RawMessage, 1)}
	d.publish()
	return d
}

func (*display) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: capschema.DisplayScreenID, Version: 1, ConfigSchema: capschema.DisplayScreenConfig,
		StateSchema: capschema.DisplayScreenState, Commands: capschema.DisplayScreenCommands,
	}
}

func (d *display) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	return d.s.applyConfig(capschema.DisplayScreenID, displayConfig, cfg)
}

func (d *display) State() <-chan json.RawMessage { return d.state }

var toastKinds = map[string]screenv1.ToastKind{
	"": screenv1.ToastKind_TOAST_KIND_INFO, "info": screenv1.ToastKind_TOAST_KIND_INFO,
	"success": screenv1.ToastKind_TOAST_KIND_SUCCESS, "warning": screenv1.ToastKind_TOAST_KIND_WARNING,
	"danger": screenv1.ToastKind_TOAST_KIND_DANGER,
}

var toastActionKinds = map[string]screenv1.ToastActionKind{
	"open": screenv1.ToastActionKind_TOAST_ACTION_KIND_OPEN, "rpc": screenv1.ToastActionKind_TOAST_ACTION_KIND_RPC,
}

var showRoutes = map[string]cadencev2.ScreenRoute{
	"home": cadencev2.ScreenRoute_SCREEN_ROUTE_HOME, "search": cadencev2.ScreenRoute_SCREEN_ROUTE_SEARCH,
	"title": cadencev2.ScreenRoute_SCREEN_ROUTE_TITLE, "episodes": cadencev2.ScreenRoute_SCREEN_ROUTE_EPISODES,
	"panel": cadencev2.ScreenRoute_SCREEN_ROUTE_PANEL, "queue": cadencev2.ScreenRoute_SCREEN_ROUTE_QUEUE,
	"tasks": cadencev2.ScreenRoute_SCREEN_ROUTE_TASKS, "task": cadencev2.ScreenRoute_SCREEN_ROUTE_TASK,
	"settings": cadencev2.ScreenRoute_SCREEN_ROUTE_SETTINGS,
}

func (d *display) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	sc, ok := displayCommands[name]
	if !ok {
		return nil, capability.ErrUnsupported
	}
	var a struct {
		Text       string `json:"text"`
		Kind       string `json:"kind"`
		Icon       string `json:"icon"`
		DurationMs uint32 `json:"durationMs"`
		Route      string `json:"route"`
		Ref        string `json:"ref"`
		Query      string `json:"query"`
		Episode    string `json:"episode"`
		Action     *struct {
			ID    string `json:"id"`
			Label string `json:"label"`
			Kind  string `json:"kind"`
			Route string `json:"route"`
			Ref   string `json:"ref"`
		} `json:"action"`
	}
	if !sc.valid(args, &a) {
		return nil, errInvalid
	}
	switch name {
	case "wake", "sleep":
		return nil, d.s.request(ctx, func(id string) *screenv1.Envelope {
			return &screenv1.Envelope{Body: &screenv1.Envelope_DisplayPower{DisplayPower: &screenv1.DisplayPower{
				RequestId: id, On: name == "wake",
			}}}
		})
	case "toast":
		// a toast has no answer: it shows or the screen is not there
		t := &screenv1.Toast{
			Id: "t-" + strconv.FormatUint(d.s.nextID.Add(1), 10), Kind: toastKinds[a.Kind], Text: a.Text,
			Icon: a.Icon, DurationMs: a.DurationMs,
		}
		if act := a.Action; act != nil {
			t.Action = &screenv1.ToastAction{Id: act.ID, Label: act.Label, Kind: toastActionKinds[act.Kind], Ref: act.Ref}
			if act.Kind == "open" {
				t.Action.Route = showRoutes[act.Route]
			} else {
				// a screen before typed actions calls toast.action with these
				t.ActionId, t.ActionLabel = act.ID, act.Label
			}
		}
		if !d.s.send(&screenv1.Envelope{Body: &screenv1.Envelope_Toast{Toast: t}}) {
			return nil, &capability.CommandError{Code: codeUnavailable}
		}
		return nil, nil
	default:
		return nil, d.s.request(ctx, func(id string) *screenv1.Envelope {
			return &screenv1.Envelope{Body: &screenv1.Envelope_Route{Route: &screenv1.Route{
				RequestId: id, Route: showRoutes[a.Route], Ref: a.Ref, Query: a.Query, Episode: a.Episode,
				Back: a.Route == "back",
			}}}
		})
	}
}

var powerNames = map[screenv1.ScreenPower]string{
	screenv1.ScreenPower_SCREEN_POWER_ON: "on", screenv1.ScreenPower_SCREEN_POWER_AMBIENT: "ambient",
	screenv1.ScreenPower_SCREEN_POWER_SLEEP: "sleep",
}

var routeNames = map[cadencev2.ScreenRoute]string{
	cadencev2.ScreenRoute_SCREEN_ROUTE_HOME: "home", cadencev2.ScreenRoute_SCREEN_ROUTE_SEARCH: "search",
	cadencev2.ScreenRoute_SCREEN_ROUTE_TITLE: "title", cadencev2.ScreenRoute_SCREEN_ROUTE_EPISODES: "episodes",
	cadencev2.ScreenRoute_SCREEN_ROUTE_PICKER: "picker", cadencev2.ScreenRoute_SCREEN_ROUTE_RESOLVING: "resolving",
	cadencev2.ScreenRoute_SCREEN_ROUTE_PLAYER: "player", cadencev2.ScreenRoute_SCREEN_ROUTE_AUDIO: "audio",
	cadencev2.ScreenRoute_SCREEN_ROUTE_PANEL: "panel", cadencev2.ScreenRoute_SCREEN_ROUTE_QUEUE: "queue",
	cadencev2.ScreenRoute_SCREEN_ROUTE_TASKS: "tasks", cadencev2.ScreenRoute_SCREEN_ROUTE_TASK: "task",
	cadencev2.ScreenRoute_SCREEN_ROUTE_SETTINGS: "settings", cadencev2.ScreenRoute_SCREEN_ROUTE_AMBIENT: "ambient",
	cadencev2.ScreenRoute_SCREEN_ROUTE_ENROLL: "enroll", cadencev2.ScreenRoute_SCREEN_ROUTE_AIRPLAY: "airplay",
}

type displayMic struct {
	Kind     string `json:"kind"`
	DeviceID string `json:"deviceId,omitempty"`
}

var micKinds = map[cadencev2.MicSourceKind]string{
	cadencev2.MicSourceKind_MIC_SOURCE_KIND_NONE: "none", cadencev2.MicSourceKind_MIC_SOURCE_KIND_SPEAKER: "speaker",
	cadencev2.MicSourceKind_MIC_SOURCE_KIND_LOCAL: "local",
}

type displayState struct {
	Power        string      `json:"power"`
	Route        string      `json:"route,omitempty"`
	DrawerOpen   bool        `json:"drawerOpen"`
	Width        uint32      `json:"width,omitempty"`
	Height       uint32      `json:"height,omitempty"`
	RefreshMhz   uint32      `json:"refreshMhz,omitempty"`
	HDR          []string    `json:"hdr,omitempty"`
	Screen       bool        `json:"screen"`
	MaxHeight    uint32      `json:"maxHeight,omitempty"`
	Codecs       []string    `json:"codecs,omitempty"`
	CardVersions []uint32    `json:"cardVersions,omitempty"`
	CardFeatures []string    `json:"cardFeatures,omitempty"`
	Mic          *displayMic `json:"mic,omitempty"`
}

// a card feature as display.screen's state takes it (pkg/cards' feature names)
var cardFeature = regexp.MustCompile(`^[a-z][a-z_.]{0,31}$`)

// publish says what the screen is now: a board without one attached shows none (screen false, asleep), so the server
// picks no video rendition for it
func (d *display) publish() {
	s := d.s
	s.mu.Lock()
	hello, status, choice := s.hello, s.status, s.mic
	s.mu.Unlock()
	st := displayState{Power: "sleep"}
	if hello != nil {
		disp := hello.GetDisplay()
		if status.GetDisplay() != nil {
			disp = status.GetDisplay()
		}
		st = displayState{
			Power: "on", Screen: true, Width: disp.GetWidth(), Height: disp.GetHeight(), RefreshMhz: disp.GetRefreshMhz(),
			HDR: capped(disp.GetHdr(), 8, 32), MaxHeight: hello.GetMaxVideoHeight(),
			Codecs: capped(hello.GetVideoCodecs(), 16, 16), DrawerOpen: status.GetDrawerOpen(),
			Route: routeNames[status.GetRoute()],
		}
		if p, ok := powerNames[status.GetPower()]; ok {
			st.Power = p
		}
		if v := hello.GetCardVersions(); len(v) > 0 {
			st.CardVersions = v[:min(len(v), 8)]
		}
		for _, f := range hello.GetCardFeatures() {
			if len(st.CardFeatures) < 48 && cardFeature.MatchString(f) {
				st.CardFeatures = append(st.CardFeatures, f)
			}
		}
		if hello.GetLocalMic() {
			st.Mic = &displayMic{Kind: "local"}
		}
		// the owner's pick in the room page wins; a local pick needs the mic attached
		if k, ok := micKinds[choice.GetKind()]; ok && (k != "local" || hello.GetLocalMic()) {
			st.Mic = &displayMic{Kind: k}
			if k == "speaker" && len(choice.GetDeviceId()) <= 64 {
				st.Mic.DeviceID = choice.GetDeviceId()
			}
		}
	}
	offer(d.state, encode(st))
}

// capped keeps at most n strings of at most size bytes, as the state's schema bounds them
func capped(in []string, n, size int) []string {
	var out []string
	for _, v := range in {
		if len(out) == n {
			break
		}
		if v != "" && len(v) <= size {
			out = append(out, v)
		}
	}
	return out
}

// video is media.video: the screen's player. its sessions are media.player's like any other once they hold the speaker
type video struct {
	s     *Server
	state chan json.RawMessage

	mu sync.Mutex
	// what play asked for, by session id, for the session state the server gets (memory-only, D11)
	plays   map[string]*screenv1.MediaPlay
	session string
	tracksN []*cadencev2.MediaTrack
}

func newVideo(s *Server) *video {
	v := &video{s: s, state: make(chan json.RawMessage, 1), plays: map[string]*screenv1.MediaPlay{}}
	v.publish()
	return v
}

func (*video) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: capschema.MediaVideoID, Version: 1, ConfigSchema: capschema.MediaVideoConfig,
		StateSchema: capschema.MediaVideoState, Commands: capschema.MediaVideoCommands,
	}
}

func (v *video) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	return v.s.applyConfig(capschema.MediaVideoID, videoConfig, cfg)
}

func (v *video) State() <-chan json.RawMessage { return v.state }

var subtitleFormats = map[string]screenv1.SubtitleFormat{
	"srt": screenv1.SubtitleFormat_SUBTITLE_FORMAT_SRT, "vtt": screenv1.SubtitleFormat_SUBTITLE_FORMAT_VTT,
	"ass": screenv1.SubtitleFormat_SUBTITLE_FORMAT_ASS,
}

var skipKinds = map[string]screenv1.SkipKind{
	"opening": screenv1.SkipKind_SKIP_KIND_OPENING, "ending": screenv1.SkipKind_SKIP_KIND_ENDING,
	"recap": screenv1.SkipKind_SKIP_KIND_RECAP, "preview": screenv1.SkipKind_SKIP_KIND_PREVIEW,
}

var trackKinds = map[string]cadencev2.MediaTrackKind{
	"video": cadencev2.MediaTrackKind_MEDIA_TRACK_KIND_VIDEO, "audio": cadencev2.MediaTrackKind_MEDIA_TRACK_KIND_AUDIO,
	"subtitle": cadencev2.MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE,
}

type playArgs struct {
	URL       string            `json:"url"`
	AudioURL  string            `json:"audioUrl"`
	Headers   map[string]string `json:"headers"`
	Subtitles []struct {
		URL      string `json:"url"`
		Language string `json:"language"`
		Label    string `json:"label"`
		Format   string `json:"format"`
		Default  bool   `json:"default"`
	} `json:"subtitles"`
	StartMs    uint64 `json:"startMs"`
	Live       bool   `json:"live"`
	Renditions []struct {
		Name     string   `json:"name"`
		Height   uint32   `json:"height"`
		FPS      uint32   `json:"fps"`
		Codecs   []string `json:"codecs"`
		Selected bool     `json:"selected"`
		HDR      string   `json:"hdr"`
	} `json:"renditions"`
	View         string   `json:"view"`
	Views        []string `json:"views"`
	AudioChoices []struct {
		ID       string `json:"id"`
		Language string `json:"language"`
		Label    string `json:"label"`
		Original bool   `json:"original"`
		Dubbed   bool   `json:"dubbed"`
		Selected bool   `json:"selected"`
	} `json:"audioChoices"`
	Skips []struct {
		Kind    string `json:"kind"`
		StartMs uint64 `json:"startMs"`
		EndMs   uint64 `json:"endMs"`
	} `json:"skips"`
	Actions []string `json:"actions"`
	// a follower's play (multiroom P4): the group whose stream its picture follows
	Follow *struct {
		GroupID string `json:"groupId"`
	} `json:"follow"`
	Metadata struct {
		Title          string `json:"title"`
		Series         string `json:"series"`
		Artist         string `json:"artist"`
		Channel        string `json:"channel"`
		Provider       string `json:"provider"`
		ProviderName   string `json:"providerName"`
		ProviderSource string `json:"providerSource"`
		Episode        string `json:"episode"`
		EpisodeName    string `json:"episodeName"`
		ArtworkURL     string `json:"artworkUrl"`
		ItemRef        string `json:"itemRef"`
		Album          string `json:"album"`
		Kind           string `json:"kind"`
	} `json:"metadata"`
}

var mediaViews = map[string]screenv1.MediaView{
	"video": screenv1.MediaView_MEDIA_VIEW_VIDEO, "audio": screenv1.MediaView_MEDIA_VIEW_AUDIO,
	"visualizer": screenv1.MediaView_MEDIA_VIEW_VISUALIZER,
}

var prepareStates = map[string]screenv1.PrepareState{
	"resolving": screenv1.PrepareState_PREPARE_STATE_RESOLVING, "failed": screenv1.PrepareState_PREPARE_STATE_FAILED,
}

// Command plays a url on the screen (paused until the gate opens, rule 7), lists its tracks or selects one. urls and
// headers are never logged
func (v *video) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	sc, ok := videoCommands[name]
	if !ok {
		return nil, capability.ErrUnsupported
	}
	switch name {
	case "play":
		var a playArgs
		if !sc.valid(args, &a) {
			return nil, errInvalid
		}
		return v.play(ctx, a)
	case "prepare":
		var a struct {
			State       string `json:"state"`
			Title       string `json:"title"`
			Subtitle    string `json:"subtitle"`
			ArtworkURL  string `json:"artworkUrl"`
			Code        string `json:"code"`
			Stage       string `json:"stage"`
			ElapsedMS   int64  `json:"elapsedMs"`
			RemainingMS int64  `json:"remainingMs"`
			Height      uint32 `json:"height"`
			VCodec      string `json:"vcodec"`
			ACodec      string `json:"acodec"`
			AudioTracks uint32 `json:"audioTracks"`
			Done        bool   `json:"done"`
			Failed      bool   `json:"failed"`
		}
		if !sc.valid(args, &a) {
			return nil, errInvalid
		}
		// no answer: it shows, or the screen is not there
		if !v.s.send(&screenv1.Envelope{Body: &screenv1.Envelope_MediaPreparing{MediaPreparing: &screenv1.MediaPreparing{
			State: prepareStates[a.State], Title: a.Title, Subtitle: a.Subtitle, ArtworkUrl: a.ArtworkURL, Code: a.Code,
			Stage: a.Stage, ElapsedMs: a.ElapsedMS, RemainingMs: a.RemainingMS, Height: a.Height, Vcodec: a.VCodec,
			Acodec: a.ACodec, AudioTracks: a.AudioTracks, Done: a.Done, Failed: a.Failed,
		}}}) {
			return nil, &capability.CommandError{Code: codeUnavailable}
		}
		return nil, nil
	case "stop":
		var a struct {
			SessionID string `json:"sessionId"`
		}
		if !sc.valid(args, &a) {
			return nil, errInvalid
		}
		return nil, v.stop(ctx, a.SessionID)
	case "tracks":
		if !sc.valid(args, nil) {
			return nil, errInvalid
		}
		v.mu.Lock()
		defer v.mu.Unlock()
		return encode(map[string]any{"tracks": trackStates(v.tracksN)}), nil
	default:
		var a struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		}
		if !sc.valid(args, &a) {
			return nil, errInvalid
		}
		return nil, v.s.request(ctx, func(id string) *screenv1.Envelope {
			return &screenv1.Envelope{Body: &screenv1.Envelope_MediaCommand{MediaCommand: &screenv1.MediaCommand{
				RequestId: id, Action: screenv1.MediaAction_MEDIA_ACTION_SELECT_TRACK, TrackId: a.ID,
				TrackKind: trackKinds[a.Kind],
			}}}
		})
	}
}

func (v *video) play(ctx context.Context, a playArgs) (json.RawMessage, error) {
	session := "video-" + strconv.FormatUint(v.s.nextID.Add(1), 10)
	mp := &screenv1.MediaPlay{
		SessionId: session, Purpose: screenv1.MediaPurpose_MEDIA_PURPOSE_CONTENT, Url: a.URL, AudioUrl: a.AudioURL,
		Headers: a.Headers, StartMs: a.StartMs, Live: a.Live, Playable: cadencev2.PlayableKind_PLAYABLE_KIND_STREAM,
		Metadata: &cadencev2.MediaMetadata{
			Title: a.Metadata.Title, Series: a.Metadata.Series, Artist: a.Metadata.Artist, Episode: a.Metadata.Episode,
			EpisodeName: a.Metadata.EpisodeName,
			ArtworkUrl:  a.Metadata.ArtworkURL, ItemRef: a.Metadata.ItemRef, Kind: cadencev2.MediaKind_MEDIA_KIND_VIDEO,
			Album: a.Metadata.Album,
		},
		View: mediaViews[a.View],
	}
	for _, name := range a.Views {
		mp.Views = append(mp.Views, mediaViews[name])
	}
	for _, c := range a.AudioChoices {
		mp.AudioChoices = append(mp.AudioChoices, &screenv1.AudioChoice{
			Id: c.ID, Language: c.Language, Label: c.Label, Original: c.Original, Dubbed: c.Dubbed, Selected: c.Selected,
		})
	}
	// the screen names the provider and shows the channel (its badge, the osd's line under the title)
	if a.Metadata.Provider != "" {
		mp.Metadata.Provider = &cadencev2.MediaProvider{Id: a.Metadata.Provider, Name: a.Metadata.ProviderName,
			Source: a.Metadata.ProviderSource}
	}
	if a.Metadata.Channel != "" {
		mp.Metadata.Channel = &cadencev2.MediaChannel{Name: a.Metadata.Channel}
	}
	if a.Metadata.Series != "" || a.Metadata.Episode != "" {
		mp.Metadata.Kind = cadencev2.MediaKind_MEDIA_KIND_EPISODE
	}
	if a.Metadata.Kind == "track" {
		// a song the server plays as audio (a music video's audio view): the screen's audio page
		mp.Metadata.Kind = cadencev2.MediaKind_MEDIA_KIND_TRACK
	}
	if a.Live {
		mp.Metadata.Kind = cadencev2.MediaKind_MEDIA_KIND_LIVE
	}
	for _, sub := range a.Subtitles {
		mp.Subtitles = append(mp.Subtitles, &screenv1.Subtitle{
			Url: sub.URL, Language: sub.Language, Label: sub.Label, Format: subtitleFormats[sub.Format], IsDefault: sub.Default,
		})
	}
	for _, r := range a.Renditions {
		mp.Renditions = append(mp.Renditions, &screenv1.Rendition{
			Name: r.Name, Height: r.Height, Fps: r.FPS, Codecs: r.Codecs, Selected: r.Selected, Hdr: r.HDR,
		})
	}
	for _, k := range a.Skips {
		mp.Skips = append(mp.Skips, &screenv1.Skip{Kind: skipKinds[k.Kind], StartMs: k.StartMs, EndMs: k.EndMs})
	}
	mp.Actions = a.Actions
	if a.Follow != nil {
		mp.FollowGroupId = a.Follow.GroupID
	}
	v.mu.Lock()
	v.plays[session] = mp
	v.mu.Unlock()
	// the session this play displaces, read before the screen can report the new one (the player is one, so what it
	// played is over the moment the screen takes this play)
	prev := v.current()
	err := v.s.request(ctx, func(id string) *screenv1.Envelope {
		mp.RequestId = id
		return &screenv1.Envelope{Body: &screenv1.Envelope_MediaPlay{MediaPlay: mp}}
	})
	if err != nil {
		v.mu.Lock()
		delete(v.plays, session)
		v.mu.Unlock()
		return nil, err
	}
	v.endReplaced(prev, session)
	return encode(map[string]string{"sessionId": session}), nil
}

// current is the session the screen's player last reported
func (v *video) current() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.session
}

// endReplaced ends old, the session a play of session displaced. left registered, the media plane holds it as a
// source the new play displaces, and the pause that asks of it (audiod's arbitration, in flight with the new play)
// would reach the player, which plays the new session now: a command to the screen names no session of its own
func (v *video) endReplaced(old, session string) {
	if old == "" || old == session {
		return
	}
	v.mu.Lock()
	delete(v.plays, old)
	cleared := v.session == old
	if cleared {
		v.session, v.tracksN = "", nil
	}
	v.mu.Unlock()
	if cleared {
		v.publish()
	}
	v.s.mu.Lock()
	delete(v.s.sessions, old)
	delete(v.s.served, old)
	v.s.mu.Unlock()
	if v.s.o.Media != nil {
		v.s.o.Media.RemoveExternal(old)
	}
}

// playing is whether the screen's player holds session, as it last reported
func (v *video) playing(session string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return session != "" && v.session == session
}

// stop ends the screen's own session id (a follower's, whose item the group no longer plays); one that is not the
// screen's session now is left alone, and answers nothing
func (v *video) stop(ctx context.Context, session string) error {
	v.mu.Lock()
	current := v.session == session
	v.mu.Unlock()
	if !current {
		return nil
	}
	return v.s.request(ctx, func(id string) *screenv1.Envelope {
		return &screenv1.Envelope{Body: &screenv1.Envelope_MediaCommand{MediaCommand: &screenv1.MediaCommand{
			RequestId: id, Action: screenv1.MediaAction_MEDIA_ACTION_STOP,
		}}}
	})
}

// following is the group a session's picture follows, "" for a play of the screen's own (multiroom P4)
func (v *video) following(session string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.plays[session].GetFollowGroupId()
}

// played is the metadata and the server's actions a session was played with
func (v *video) played(session string) (*cadencev2.MediaMetadata, []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	mp, ok := v.plays[session]
	if !ok {
		return nil, nil
	}
	return proto.CloneOf(mp.GetMetadata()), mp.GetActions()
}

// tracks follows the screen's player: its tracks are media.video's state, and an idle player forgets its session
func (v *video) tracks(ms *screenv1.MediaState) {
	if ms.GetPurpose() == screenv1.MediaPurpose_MEDIA_PURPOSE_AMBIENT {
		return
	}
	v.mu.Lock()
	if ms.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_IDLE {
		delete(v.plays, ms.GetSessionId())
		v.session, v.tracksN = "", nil
	} else {
		v.session, v.tracksN = ms.GetSessionId(), ms.GetTracks()
	}
	v.mu.Unlock()
	v.publish()
}

func (v *video) clear() {
	v.mu.Lock()
	v.plays = map[string]*screenv1.MediaPlay{}
	v.session, v.tracksN = "", nil
	v.mu.Unlock()
	v.publish()
}

type trackState struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Language string `json:"language,omitempty"`
	Label    string `json:"label,omitempty"`
	Selected bool   `json:"selected"`
	External bool   `json:"external,omitempty"`
}

var trackNames = map[cadencev2.MediaTrackKind]string{
	cadencev2.MediaTrackKind_MEDIA_TRACK_KIND_VIDEO: "video", cadencev2.MediaTrackKind_MEDIA_TRACK_KIND_AUDIO: "audio",
	cadencev2.MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE: "subtitle",
}

// trackStates are the tracks the state's schema takes: at most 64, each within its bounds
func trackStates(ts []*cadencev2.MediaTrack) []trackState {
	out := []trackState{}
	for _, t := range ts {
		kind, ok := trackNames[t.GetKind()]
		if !ok || t.GetId() == "" || len(t.GetId()) > 64 || len(t.GetLanguage()) > 35 || len(t.GetLabel()) > 128 {
			continue
		}
		out = append(out, trackState{t.GetId(), kind, t.GetLanguage(), t.GetLabel(), t.GetSelected(), t.GetExternal()})
		if len(out) == 64 {
			break
		}
	}
	return out
}

type videoFollow struct {
	GroupID string `json:"groupId"`
}

// groupID is a group id as media.video's state takes it
var groupID = regexp.MustCompile(`^[0-9a-f-]{36}$`)

// publish says the session and its tracks, and the group a follower's session follows: the server sees a follower
// that left (its own play, the player closed) by its state
func (v *video) publish() {
	v.mu.Lock()
	st := struct {
		SessionID string       `json:"sessionId,omitempty"`
		Tracks    []trackState `json:"tracks"`
		Follow    *videoFollow `json:"follow,omitempty"`
	}{SessionID: v.session, Tracks: trackStates(v.tracksN)}
	if g := v.plays[v.session].GetFollowGroupId(); v.session != "" && groupID.MatchString(g) {
		st.Follow = &videoFollow{GroupID: g}
	}
	v.mu.Unlock()
	if len(st.SessionID) > 64 {
		st.SessionID, st.Follow = "", nil
	}
	offer(v.state, encode(st))
}

// remote is input.remote: keys and text from the server (HA, the dashboard), injected as if pressed. a key never
// moves the device's own level (rule 7): the screen hands volume keys to the tv (cec) in the software volume mode
type remote struct {
	s     *Server
	state chan json.RawMessage

	mu   sync.Mutex
	last string
}

func newRemote(s *Server) *remote {
	r := &remote{s: s, state: make(chan json.RawMessage, 1), last: "none"}
	r.publish()
	return r
}

func (*remote) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: capschema.InputRemoteID, Version: 1, ConfigSchema: capschema.InputRemoteConfig,
		StateSchema: capschema.InputRemoteState, Commands: capschema.InputRemoteCommands,
	}
}

func (r *remote) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	return r.s.applyConfig(capschema.InputRemoteID, remoteConfig, cfg)
}

func (r *remote) State() <-chan json.RawMessage { return r.state }

var keyNames = func() map[string]screenv1.Key {
	m := map[string]screenv1.Key{}
	for i, name := range capschema.Keys {
		m[name] = screenv1.Key(i + 1)
	}
	return m
}()

var keyActions = map[string]screenv1.KeyAction{
	"": screenv1.KeyAction_KEY_ACTION_PRESS, "press": screenv1.KeyAction_KEY_ACTION_PRESS,
	"down": screenv1.KeyAction_KEY_ACTION_DOWN, "up": screenv1.KeyAction_KEY_ACTION_UP,
	"long": screenv1.KeyAction_KEY_ACTION_LONG,
}

// Command injects a key or text into the screen; text is never logged
func (r *remote) Command(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	sc, ok := remoteCommands[name]
	if !ok {
		return nil, capability.ErrUnsupported
	}
	var a struct {
		Key    string `json:"key"`
		Action string `json:"action"`
		Value  string `json:"value"`
	}
	if !sc.valid(args, &a) {
		return nil, errInvalid
	}
	var env *screenv1.Envelope
	if name == "key" {
		env = &screenv1.Envelope{Body: &screenv1.Envelope_InputEvent{InputEvent: &screenv1.InputEvent{
			Key: keyNames[a.Key], Action: keyActions[a.Action], Source: screenv1.InputSource_INPUT_SOURCE_API,
		}}}
	} else {
		env = &screenv1.Envelope{Body: &screenv1.Envelope_TextInput{TextInput: &screenv1.TextInput{Text: a.Value}}}
	}
	if !r.s.send(env) {
		return nil, &capability.CommandError{Code: codeUnavailable}
	}
	r.saw(screenv1.InputSource_INPUT_SOURCE_API)
	return nil, nil
}

var sourceNames = map[screenv1.InputSource]string{
	screenv1.InputSource_INPUT_SOURCE_REMOTE: "remote", screenv1.InputSource_INPUT_SOURCE_CEC: "cec",
	screenv1.InputSource_INPUT_SOURCE_KEYBOARD: "keyboard", screenv1.InputSource_INPUT_SOURCE_GAMEPAD: "gamepad",
	screenv1.InputSource_INPUT_SOURCE_API: "api",
}

func (r *remote) saw(src screenv1.InputSource) {
	name, ok := sourceNames[src]
	if !ok {
		return
	}
	r.mu.Lock()
	changed := r.last != name
	r.last = name
	r.mu.Unlock()
	if changed {
		r.publish()
	}
}

func (r *remote) publish() {
	r.mu.Lock()
	last := r.last
	r.mu.Unlock()
	offer(r.state, encode(map[string]string{"lastSource": last}))
}

// Screencast is media.screencast's config as the board's receiver takes it (libairplay in audiod)
type Screencast struct {
	Enabled bool
	// "" is the receiver's default name
	Name string
	Pin  bool
	// 720, 1080 or 2160; 0 is the screen's own size
	MaxHeight uint32
	H265      bool
}

// MaxWidth is the 16:9 width of MaxHeight, 0 for the screen's own
func (c Screencast) MaxWidth() uint32 { return c.MaxHeight * 16 / 9 }

// screencast is media.screencast: airplay on the screen (libairplay in audiod). its config goes to the screen and to
// the board's receiver (Options.Screencast); its state is the receiver's as audiod reports it, and whether a sender
// mirrors, its name memory-only on the server (D11)
type screencast struct {
	s     *Server
	state chan json.RawMessage

	mu        sync.Mutex
	enabled   bool
	mirroring bool
	client    string
	// the receiver's state and code; "" until a board's receiver reports one
	receiver string
	code     string
}

func newScreencast(s *Server) *screencast {
	c := &screencast{s: s, state: make(chan json.RawMessage, 1)}
	c.publish()
	return c
}

func (*screencast) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: capschema.MediaScreencastID, Version: 1, ConfigSchema: capschema.MediaScreencastConfig,
		StateSchema: capschema.MediaScreencastState,
	}
}

func (c *screencast) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	var a struct {
		Enabled   *bool   `json:"enabled"`
		Name      *string `json:"name"`
		Pin       *bool   `json:"pin"`
		MaxHeight *uint32 `json:"maxHeight"`
		H265      *bool   `json:"h265"`
	}
	if !screencastConfig.valid(cfg, &a) {
		return &capability.ApplyError{Code: codeInvalid}
	}
	if err := c.s.applyConfig(capschema.MediaScreencastID, screencastConfig, cfg); err != nil {
		return err
	}
	sc := Screencast{
		Enabled: a.Enabled != nil && *a.Enabled, Name: deref(a.Name), Pin: a.Pin != nil && *a.Pin,
		MaxHeight: deref(a.MaxHeight), H265: a.H265 != nil && *a.H265,
	}
	c.mu.Lock()
	c.enabled = sc.Enabled
	c.mu.Unlock()
	c.publish()
	if c.s.o.Screencast != nil {
		c.s.o.Screencast(sc)
	}
	return nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func (*screencast) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (c *screencast) State() <-chan json.RawMessage { return c.state }

func (c *screencast) setMirroring(active bool, client string) {
	c.mu.Lock()
	c.mirroring = active
	c.client = ""
	if active && len(client) <= 64 {
		c.client = client
	}
	c.mu.Unlock()
	c.publish()
}

func (c *screencast) setReceiver(state, code string) {
	c.mu.Lock()
	changed := c.receiver != state || c.code != code
	c.receiver, c.code = state, code
	c.mu.Unlock()
	if changed {
		c.publish()
	}
}

func (c *screencast) publish() {
	c.mu.Lock()
	st := struct {
		Enabled    bool   `json:"enabled"`
		State      string `json:"state"`
		Code       string `json:"code,omitempty"`
		Mirroring  bool   `json:"mirroring"`
		ClientName string `json:"clientName,omitempty"`
	}{Enabled: c.enabled, State: "off", Mirroring: c.mirroring, ClientName: c.client}
	switch {
	case !c.enabled:
	case c.receiver != "":
		st.State, st.Code = c.receiver, c.code
	case c.mirroring:
		st.State = "ready"
	default:
		// no receiver reported yet (none on this board, or audiod not up)
		st.State = "starting"
	}
	c.mu.Unlock()
	offer(c.state, encode(st))
}
