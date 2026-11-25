// Package video plays YouTube videos on a device: a screen (display.screen
// state with screen: true, and media.video) gets a rendition set through the
// stream relay and a media.video play; any other device gets the audio as a
// media.url session, as music does. The session's renditions stay in memory
// (D11) so a select_track of a video rendition plays it from the same point.
// Screens play through mpv into audiod's mixer (audio route MIXER); this
// package never sends a volume (rule 7). Lines carry ids, codes, counts and
// timings, never a title, a query or a URL.
package video

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/streams"
)

const (
	CapVideo  = "media.video"
	CapURL    = "media.url"
	CapScreen = "display.screen"
	// the device's media.url and media.video bounds
	maxDeviceURL = 2047
	maxTitle     = 256
	// a relay token lives past the film's length by this much, and at least 6 h
	ttlMargin = 3 * time.Hour
	minTTL    = 6 * time.Hour
	liveTTL   = 12 * time.Hour
)

var (
	ErrNotSetUp = errors.New("video: no player set up")
	// ErrNoScreen is a device without a screen that plays video, asked for one
	ErrNoScreen = errors.New("video: no screen")
	// ErrNoPlayer is a device that plays neither video nor a url
	ErrNoPlayer = errors.New("video: the device plays no media")
	// ErrNoSession is a device the service started no video on, or whose renditions expired
	ErrNoSession = errors.New("video: no session")
	// ErrUnknownRendition is an id the session does not hold
	ErrUnknownRendition = errors.New("video: unknown rendition")
	// ErrUnknownAudio is an audio language the session does not hold
	ErrUnknownAudio = errors.New("video: unknown audio")
	// ErrUnknownView is a view the session cannot switch to (a video that is not music)
	ErrUnknownView = errors.New("video: unknown view")
)

// Player is what the musicPlayer instance (yt-dlp) does for video.
type Player interface {
	providers.Player
	ResolveScreen(ctx context.Context, videoID string, p catalog.DeviceProfile) (ytdlp.Screen, error)
	SearchVideos(ctx context.Context, query string, limit int) ([]ytdlp.Video, error)
}

// Relay keeps the upstreams a device fetches (streams.Relay).
type Relay interface {
	AddSet(deviceID string, ups []streams.Upstream, o streams.Options) (string, error)
	Drop(token string)
}

type Deps struct {
	Settings func(ctx context.Context, deviceID string) (settings.Values, error)
	// Player builds the instance the musicPlayer setting names
	Player func(ctx context.Context, name string) (providers.Player, error)
	// State is a capability's last reported state
	State func(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)
	// Reports is nil when the device's report lists the capability's command
	Reports func(ctx context.Context, deviceID, capability, command string) error
	// Relay and Origin have the device fetch through the server (Origin + "/v2/stream/…"); without an origin the
	// device fetches the upstream urls itself
	Relay  Relay
	Origin string
	// Command sends a capability command to the device, past Intercept
	Command func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
	// Session is the device's media session as the server last saw it
	Session func(deviceID string) (media.MediaSession, bool)
	// Image is the url a screen fetches an image through (the server's image proxy: origin + /v2/images/<id>), ""
	// for a url it refuses; nil hands the url out itself
	Image func(rawURL string) string
	Log   *slog.Logger
	Now   func() time.Time
}

type Service struct {
	d Deps

	mu       sync.Mutex
	sessions map[string]*session
	plays    map[string]*sync.Mutex
}

// rendition is one quality as the device fetches it
type rendition struct {
	RenditionView
	url, audioURL string
	acodec        string
}

// audioTrack is one audio language as the device fetches it
type audioTrack struct {
	AudioView
	url    string
	acodec string
}

type session struct {
	token      string
	renditions []rendition
	selected   int
	// audio is the session's audio languages (none for a muxed or live video); audioSel the one playing
	audio    []audioTrack
	audioSel int
	// views are the views the session switches between (a music video's), view the one it plays in
	views []string
	view  string
	live  bool
	music bool
	meta  metadata
	info  ytdlp.Info
	// device is the device's session id the last play answered, "" when it named none
	device  string
	expires time.Time
	// req is the request the session resolved, which a group's follower screen plays again (multiroom P4)
	req Request
	// follow is the multiroom group a follower's session follows, "" for a play of the device's own
	follow string
}

func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d, sessions: map[string]*session{}, plays: map[string]*sync.Mutex{}}
}

func (s *Service) playLock(deviceID string) func() {
	s.mu.Lock()
	m := s.plays[deviceID]
	if m == nil {
		m = &sync.Mutex{}
		s.plays[deviceID] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// Profile is the device's profile from its display.screen state; Screen is false for a device whose state says no
// screen or that reports no media.video play.
func (s *Service) Profile(ctx context.Context, deviceID string) catalog.DeviceProfile {
	var p catalog.DeviceProfile
	raw, ok := s.d.State(ctx, deviceID, CapScreen)
	if !ok {
		return p
	}
	var st struct {
		Screen    bool     `json:"screen"`
		MaxHeight int      `json:"maxHeight"`
		Codecs    []string `json:"codecs"`
	}
	if json.Unmarshal(raw, &st) != nil || !st.Screen || s.d.Reports(ctx, deviceID, CapVideo, "play") != nil {
		return p
	}
	return catalog.DeviceProfile{Screen: true, MaxHeight: st.MaxHeight, Codecs: st.Codecs}
}

// Request is a video to play. Title, Channel and ArtworkURL are what the screen shows until the video's own are read
// (memory only).
type Request struct {
	VideoID    string
	StartMS    int64
	Title      string
	Channel    string
	ArtworkURL string
	// ScreenOnly refuses a device without a screen (ErrNoScreen) instead of playing the audio
	ScreenOnly bool
	// Music is a music.youtube.com link: a song, shown as audio with the visualizer unless View says otherwise
	Music bool
	// View is how a screen shows a music video: video, audio or visualizer; "" for its default
	View string
}

// the views a music video switches between: the picture, its audio alone (the cover), its audio with the visualizer
const (
	ViewVideo      = "video"
	ViewAudio      = "audio"
	ViewVisualizer = "visualizer"
)

// ValidView is a view a request or a switch may name
func ValidView(v string) bool { return v == ViewVideo || v == ViewAudio || v == ViewVisualizer }

// PlayResult says what played: "video" on a screen, "audio" on a speaker.
type PlayResult struct {
	Mode       string `json:"mode" enum:"video,audio"`
	Height     int    `json:"height,omitempty" doc:"The playing rendition's height; absent for audio."`
	Renditions int    `json:"renditions,omitempty" doc:"How many qualities the screen can switch between."`
	Live       bool   `json:"live,omitempty"`
	View       string `json:"view,omitempty" enum:"video,audio,visualizer" doc:"How the screen shows it; absent on a speaker."`
	Title      string `json:"title,omitempty" doc:"The video's title as YouTube names it (memory only)."`
}

// Play plays the video on the device: its renditions on a screen, its audio elsewhere.
func (s *Service) Play(ctx context.Context, deviceID string, r Request) (PlayResult, error) {
	defer s.playLock(deviceID)()
	v, err := s.d.Settings(ctx, deviceID)
	if err != nil {
		return PlayResult{}, err
	}
	if v.MusicPlayer == "" {
		return PlayResult{}, ErrNotSetUp
	}
	pp, err := s.d.Player(ctx, v.MusicPlayer)
	if err != nil {
		return PlayResult{}, ErrNotSetUp
	}
	prof := s.Profile(ctx, deviceID)
	start := s.d.Now()
	if prof.Screen {
		p, ok := pp.(Player)
		if !ok {
			return PlayResult{}, ErrNotSetUp
		}
		res, err := s.playScreen(ctx, deviceID, p, prof, r)
		s.logPlay(deviceID, "video", res, err, start)
		return res, err
	}
	if r.ScreenOnly {
		return PlayResult{}, ErrNoScreen
	}
	if s.d.Reports(ctx, deviceID, CapURL, "play") != nil {
		return PlayResult{}, ErrNoPlayer
	}
	res, err := s.playAudio(ctx, deviceID, pp, r)
	s.logPlay(deviceID, "audio", res, err, start)
	return res, err
}

func (s *Service) logPlay(deviceID, mode string, res PlayResult, err error, start time.Time) {
	ms := s.d.Now().Sub(start).Milliseconds()
	if err != nil {
		s.d.Log.Warn("video_play_failed", "device_id", deviceID, "mode", mode, "code", Code(err), "ms", ms)
		return
	}
	s.d.Log.Info("video_play", "device_id", deviceID, "mode", mode, "height", res.Height, "renditions", res.Renditions,
		"live", res.Live, "ms", ms)
}

// screenPlayTimeout bounds a screen's play on its own: yt-dlp's resolve (seconds), youtube's available_at wait and
// the screen's answer. it sits inside the play tools' 90 s budget (agent/tools playBudget), which also waits for the
// first frame, and under the screen's own waits for it (resolving's kExternalTimeoutMs, rpc's kNoAnswerMs)
const screenPlayTimeout = 75 * time.Second

// PrepareCollection shows a screen's resolving step for an album, a playlist or an artist (kind: their
// playprogress.Kind*) while its songs are read, before the first of them resolves; a device without a screen's
// prepare is left alone
func (s *Service) PrepareCollection(ctx context.Context, deviceID, kind, title, subtitle, artworkURL string) {
	if s.d.Reports(ctx, deviceID, CapVideo, "prepare") != nil {
		return
	}
	s.prepare(ctx, deviceID, preparing{State: "resolving", Title: bounded(title, maxTitle), Subtitle: bounded(subtitle, maxTitle),
		ArtworkURL: s.image(providers.MusicArtwork(artworkURL)), Stage: playprogress.Stage(kind)})
}

func (s *Service) playScreen(ctx context.Context, deviceID string, p Player, prof catalog.DeviceProfile, r Request) (PlayResult, error) {
	// a play the household asked for outlives the turn that started it: a fast mode's step deadline cancelled the
	// resolve midway, leaving the screen on a failure for a play that would have worked
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), screenPlayTimeout)
	defer cancel()
	// the screen shows its resolving step while yt-dlp runs (seconds), never an idle screen
	prepare := s.d.Reports(ctx, deviceID, CapVideo, "prepare") == nil
	if prepare {
		art := cmp.Or(r.ArtworkURL, ytdlp.Video{ID: r.VideoID}.ThumbnailURL())
		if r.Music {
			art = providers.MusicArtwork(art)
		}
		s.prepare(ctx, deviceID, preparing{State: "resolving", Title: bounded(r.Title, maxTitle), Subtitle: bounded(r.Channel, maxTitle),
			ArtworkURL: s.image(art)})
		reportCtx := ctx
		ctx = ytdlp.WithProgress(reportCtx, func(ev ytdlp.ResolveEvent) {
			remaining := ev.RemainingMS
			s.prepare(context.WithoutCancel(reportCtx), deviceID, preparing{State: "resolving", Code: ev.Code,
				Stage: ev.Stage, ElapsedMS: ev.ElapsedMS, RemainingMS: &remaining, Height: ev.Height, VCodec: ev.VCodec,
				ACodec: ev.ACodec, AudioTracks: ev.AudioTracks, Done: ev.Done, Failed: ev.Failed})
		})
	}
	sess, err := s.resolveScreen(ctx, deviceID, p, prof, r)
	if err != nil {
		if prepare {
			s.prepare(ctx, deviceID, preparing{State: "failed", Code: Code(err)})
		}
		return PlayResult{}, err
	}
	if prepare {
		// what it is, now that it is known: the screen's step names it while the stream opens
		s.prepare(ctx, deviceID, preparing{State: "resolving", Title: sess.meta.Title,
			Subtitle: cmp.Or(sess.meta.Artist, sess.meta.Channel), ArtworkURL: sess.meta.ArtworkURL})
	}
	start := r.StartMS
	if sess.live {
		start = 0
	}
	if err := s.sendVideo(ctx, deviceID, sess, start); err != nil {
		s.dropToken(sess.token)
		if prepare {
			s.prepare(ctx, deviceID, preparing{State: "failed", Code: Code(err)})
		}
		return PlayResult{}, err
	}
	s.mu.Lock()
	if old := s.sessions[deviceID]; old != nil && old.token != sess.token {
		s.dropToken(old.token)
	}
	s.sessions[deviceID] = sess
	s.mu.Unlock()
	res := PlayResult{Mode: "video", Renditions: len(sess.renditions), Live: sess.live, View: sess.view, Title: sess.meta.Title}
	if sess.view == ViewVideo {
		res.Height = sess.renditions[sess.selected].Height
	}
	return res, nil
}

// resolveScreen resolves the video and puts its streams behind the relay, with what it is and how it plays: the
// device's language's audio, else the original; a music video's view
func (s *Service) resolveScreen(ctx context.Context, deviceID string, p Player, prof catalog.DeviceProfile, r Request) (*session, error) {
	sc, err := p.ResolveScreen(ctx, r.VideoID, prof)
	if err != nil {
		return nil, err
	}
	lang := ""
	if v, err := s.d.Settings(ctx, deviceID); err == nil {
		lang = v.Language
	}
	sess, err := s.relaySet(ctx, deviceID, sc, lang)
	if err != nil {
		return nil, err
	}
	sess.info = sc.Info
	sess.music = r.Music || sc.Info.Music
	sess.view = ViewVideo
	if sess.music && !sess.live && len(sess.audio) > 0 {
		sess.views = []string{ViewVideo, ViewAudio, ViewVisualizer}
		if r.Music {
			sess.view = ViewVisualizer
		}
		if ValidView(r.View) {
			sess.view = r.View
		}
	}
	sess.meta = s.metadataOf(r, sc.Info, sess.music)
	sess.req = r
	return sess, nil
}

// relaySet puts the screen's upstreams behind one relay token: its audio languages first, then a url per rendition;
// without a relay the device gets the upstream urls themselves
func (s *Service) relaySet(ctx context.Context, deviceID string, sc ytdlp.Screen, lang string) (*session, error) {
	sess := &session{live: sc.Live, expires: sc.Expires}
	direct := s.d.Origin == "" || s.d.Relay == nil
	tracks := sc.Audio
	if len(tracks) == 0 && sc.AudioURL != "" {
		tracks = []ytdlp.AudioTrack{{ID: "a-und", Language: "und", Original: true, URL: sc.AudioURL}}
	}
	// the relay holds 16 upstreams: the renditions, then the languages that fit, the device's and the original first
	room := streams.MaxUpstreams - min(len(sc.Renditions), maxListed)
	if direct {
		room = maxAudio
	}
	var ups []streams.Upstream
	for _, a := range preferred(tracks, lang) {
		if len(sess.audio) == min(room, maxAudio) {
			break
		}
		at := audioTrack{AudioView: AudioView{ID: a.ID, Language: a.Language, Label: a.Label, Original: a.Original, Dubbed: a.Dubbed}, acodec: a.ACodec}
		if direct {
			if len(a.URL) > maxDeviceURL {
				continue
			}
			at.url = a.URL
		} else {
			at.url = strconv.Itoa(len(ups))
			ups = append(ups, streams.Upstream{URL: a.URL})
		}
		sess.audio = append(sess.audio, at)
	}
	for i, x := range sc.Renditions {
		rd := rendition{RenditionView: RenditionView{ID: renditionID(x, sc.Renditions), Height: x.Height, FPS: x.FPS, VCodec: x.VCodec, Bitrate: x.Bitrate}, acodec: x.ACodec}
		if i < len(sc.HDR) {
			rd.HDR = sc.HDR[i]
		}
		if direct {
			rd.url, rd.audioURL = x.URL, x.AudioURL
			if len(rd.url) > maxDeviceURL || len(rd.audioURL) > maxDeviceURL {
				continue
			}
			if x.AudioURL != "" && len(sess.audio) > 0 {
				rd.audioURL = ""
			}
		} else {
			if len(ups) == streams.MaxUpstreams {
				break
			}
			rd.url = strconv.Itoa(len(ups))
			ups = append(ups, streams.Upstream{URL: x.URL, HLS: sc.HLS})
		}
		sess.renditions = append(sess.renditions, rd)
	}
	if len(sess.renditions) == 0 {
		return nil, providers.ErrUnplayable
	}
	if len(ups) == 0 {
		return sess, nil
	}
	ttl := liveTTL
	if !sc.Live {
		ttl = max(minTTL, time.Duration(sc.DurationMS)*time.Millisecond+ttlMargin)
	}
	token, err := s.d.Relay.AddSet(deviceID, ups, streams.Options{TTL: ttl, OnFirstByte: func() { ytdlp.RelayFirstByte(ctx) }})
	if err != nil {
		return nil, errors.New(providers.CodePlayerFailed)
	}
	sess.token = token
	base := s.d.Origin + "/v2/stream/" + token + "/"
	for i := range sess.renditions {
		sess.renditions[i].url = base + sess.renditions[i].url
	}
	for i := range sess.audio {
		sess.audio[i].url = base + sess.audio[i].url
	}
	if exp := s.d.Now().Add(ttl); sess.expires.IsZero() || exp.Before(sess.expires) {
		sess.expires = exp
	}
	return sess, nil
}

// maxAudio is media.video/play's audioChoices maxItems
const maxAudio = 24

// preferred orders the audio languages as they are kept and chosen: the device's language first (its original
// before a dubbing), then the original, then the rest as they came; the first is the one that plays
func preferred(tracks []ytdlp.AudioTrack, lang string) []ytdlp.AudioTrack {
	primary, _, _ := strings.Cut(strings.ToLower(lang), "-")
	rank := func(a ytdlp.AudioTrack) int {
		al, _, _ := strings.Cut(strings.ToLower(a.Language), "-")
		switch {
		case primary != "" && al == primary:
			return 0
		case a.Original:
			return 1
		}
		return 2
	}
	out := slices.Clone(tracks)
	slices.SortStableFunc(out, func(a, b ytdlp.AudioTrack) int { return cmp.Compare(rank(a), rank(b)) })
	return out
}

func (s *Service) dropToken(token string) {
	if token != "" && s.d.Relay != nil {
		s.d.Relay.Drop(token)
	}
}

// renditionID names a rendition by its height ("720p"), with its fps where two share it ("720p60")
func renditionID(x catalog.Rendition, all []catalog.Rendition) string {
	id := strconv.Itoa(x.Height) + "p"
	n := 0
	for _, y := range all {
		if y.Height == x.Height {
			n++
		}
	}
	if n > 1 {
		id += strconv.Itoa(x.FPS)
	}
	return id
}

// metadata is what the screen shows of the video: its title, the channel under it, the provider's badge. a channel
// is not a series (a series makes the screen call the video an episode). a music video in an audio view is a track:
// its artist and album, on the screen's audio page
type metadata struct {
	Title        string `json:"title,omitempty"`
	Artist       string `json:"artist,omitempty"`
	Album        string `json:"album,omitempty"`
	Channel      string `json:"channel,omitempty"`
	Provider     string `json:"provider,omitempty"`
	ProviderName string `json:"providerName,omitempty"`
	ArtworkURL   string `json:"artworkUrl,omitempty"`
	Kind         string `json:"kind,omitempty"`
}

// metadataOf is the video's own title, channel and still, the request's where yt-dlp read none; a song's track,
// artist and album
func (s *Service) metadataOf(r Request, in ytdlp.Info, music bool) metadata {
	m := metadata{Title: bounded(cmp.Or(in.Title, r.Title), maxTitle), Channel: bounded(cmp.Or(in.Channel, r.Channel), maxTitle),
		Provider: "youtube", ProviderName: "YouTube", ArtworkURL: s.image(cmp.Or(in.ThumbnailURL, r.ArtworkURL))}
	if music {
		// a song's cover is square: the music catalog's own when the request brought one, else the video's frame
		// without a letterbox, which the screen's square box crops to its centre
		m.ArtworkURL = s.image(providers.MusicArtwork(cmp.Or(r.ArtworkURL, in.ThumbnailURL)))
		m.Title = bounded(cmp.Or(in.Track, in.Title, r.Title), maxTitle)
		m.Artist = bounded(cmp.Or(in.Artist, strings.TrimSuffix(m.Channel, " - Topic")), maxTitle)
		m.Album = bounded(in.Album, maxTitle)
	}
	if r.Music {
		m.Provider, m.ProviderName = "youtube_music", "YouTube Music"
	}
	return m
}

// image is the url the screen fetches rawURL by: through the server's image proxy when there is one; "" for a url
// that is not https or is too long for the screen
func (s *Service) image(rawURL string) string {
	if !strings.HasPrefix(rawURL, "https://") || len(rawURL) > maxDeviceURL {
		return ""
	}
	if s.d.Image == nil {
		return rawURL
	}
	u := s.d.Image(rawURL)
	if len(u) > maxDeviceURL {
		return ""
	}
	return u
}

func metadataOf(r Request) metadata {
	m := metadata{Title: bounded(r.Title, maxTitle), Channel: bounded(r.Channel, maxTitle), Provider: "youtube", ProviderName: "YouTube"}
	if len(r.ArtworkURL) <= maxDeviceURL && strings.HasPrefix(r.ArtworkURL, "https://") {
		m.ArtworkURL = r.ArtworkURL
	}
	return m
}

// bounded cuts s to n code points (a JSON Schema maxLength)
func bounded(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

type playVideo struct {
	URL          string          `json:"url"`
	AudioURL     string          `json:"audioUrl,omitempty"`
	StartMS      int64           `json:"startMs,omitempty"`
	Live         bool            `json:"live,omitempty"`
	Metadata     metadata        `json:"metadata"`
	Renditions   []playRendition `json:"renditions,omitempty"`
	View         string          `json:"view,omitempty"`
	Views        []string        `json:"views,omitempty"`
	AudioChoices []AudioView     `json:"audioChoices,omitempty"`
	Follow       *followArgs     `json:"follow,omitempty"`
}

// followArgs is a follower's play's group (multiroom P4)
type followArgs struct {
	GroupID string `json:"groupId"`
}

// playRendition is one quality as media.video/play lists it to the screen: never a url
type playRendition struct {
	Name     string   `json:"name"`
	Height   int      `json:"height"`
	FPS      int      `json:"fps,omitempty"`
	Codecs   []string `json:"codecs,omitempty"`
	Selected bool     `json:"selected,omitempty"`
	HDR      string   `json:"hdr,omitempty"`
}

// maxListed is media.video/play's renditions maxItems
const maxListed = 16

// listed names the session's renditions for the screen, i the one it plays; a single rendition is no choice
func listed(sess *session, i int) []playRendition {
	if len(sess.renditions) < 2 {
		return nil
	}
	out := make([]playRendition, 0, min(len(sess.renditions), maxListed))
	for j, rd := range sess.renditions {
		if len(out) == maxListed {
			break
		}
		if !renditionName.MatchString(rd.ID) || rd.Height < 1 || rd.Height > 8640 {
			continue
		}
		var codecs []string
		for _, c := range []string{rd.VCodec, rd.acodec} {
			if codecName.MatchString(c) {
				codecs = append(codecs, c)
			}
		}
		hdr := rd.HDR
		if !hdrName.MatchString(hdr) {
			hdr = ""
		}
		out = append(out, playRendition{Name: rd.ID, Height: rd.Height, FPS: min(max(rd.FPS, 0), 480), Codecs: codecs, Selected: j == i, HDR: hdr})
	}
	return out
}

// the names media.video/play takes: a height and an fps
var renditionName = regexp.MustCompile(`^[0-9]{2,4}p([0-9]{1,3})?$`)

// the codec names media.video/play takes; anything else (a yt-dlp format string) is left out
var codecName = regexp.MustCompile(`^[a-z0-9.-]{1,16}$`)

// the dynamic ranges media.video/play takes
var hdrName = regexp.MustCompile(`^[A-Za-z0-9+.-]{1,16}$`)

// the audio ids and languages media.video/play takes
var (
	audioID   = regexp.MustCompile(`^a-[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	audioLang = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
)

// choices are the session's audio languages as the screen lists them, the one playing marked; one language is no
// choice
func choices(sess *session) []AudioView {
	if len(sess.audio) < 2 {
		return nil
	}
	var out []AudioView
	for i, a := range sess.audio {
		if !audioID.MatchString(a.ID) || !audioLang.MatchString(a.Language) {
			continue
		}
		v := a.AudioView
		v.Label = bounded(v.Label, 64)
		v.Selected = i == sess.audioSel
		out = append(out, v)
	}
	return out
}

// sendVideo plays the session as it stands (its rendition, audio language and view) on the device from startMS,
// and keeps the device's session id
func (s *Service) sendVideo(ctx context.Context, deviceID string, sess *session, startMS int64) error {
	p := playVideo{StartMS: max(0, startMS), Live: sess.live, Metadata: sess.meta}
	if sess.follow != "" {
		// a follower's position is the group's stream's
		p.StartMS, p.Follow = 0, &followArgs{GroupID: sess.follow}
	}
	audio := ""
	if sess.audioSel >= 0 && sess.audioSel < len(sess.audio) {
		audio = sess.audio[sess.audioSel].url
	}
	rd := sess.renditions[sess.selected]
	if sess.view == ViewVideo || audio == "" {
		p.URL, p.AudioURL = rd.url, cmp.Or(audio, rd.audioURL)
		p.Renditions = listed(sess, sess.selected)
	} else {
		// an audio view fetches the audio alone, and the screen shows it as a track
		p.URL = audio
		p.Metadata.Kind = "track"
	}
	if len(sess.views) > 1 {
		p.View, p.Views = sess.view, sess.views
	}
	p.AudioChoices = choices(sess)
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	out, err := s.d.Command(ctx, deviceID, CapVideo, "play", raw)
	var inv *commands.InvalidArgumentsError
	if errors.As(err, &inv) && olderScreen(inv.Path) && sess.follow == "" {
		// a screen from before the channel, the views and the audio languages: its schema refuses them, so it gets
		// what it knows; never a follower's, which without its group would play aloud on its own
		raw, err = json.Marshal(playVideo{URL: p.URL, AudioURL: p.AudioURL, StartMS: p.StartMS, Live: p.Live,
			Metadata: metadata{Title: sess.meta.Title, ArtworkURL: sess.meta.ArtworkURL}, Renditions: p.Renditions})
		if err != nil {
			return err
		}
		out, err = s.d.Command(ctx, deviceID, CapVideo, "play", raw)
	}
	if err != nil {
		return err
	}
	var played struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(out, &played) == nil && len(played.SessionID) <= 64 {
		s.mu.Lock()
		sess.device = played.SessionID
		s.mu.Unlock()
	}
	return nil
}

func olderScreen(path string) bool {
	for _, p := range []string{"/metadata", "/view", "/audioChoices", "/renditions"} {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// preparing is media.video/prepare's arguments
type preparing struct {
	State      string `json:"state"`
	Title      string `json:"title,omitempty"`
	Subtitle   string `json:"subtitle,omitempty"`
	ArtworkURL string `json:"artworkUrl,omitempty"`
	Code       string `json:"code,omitempty"`

	Stage       playprogress.Stage `json:"stage,omitempty"`
	ElapsedMS   int64              `json:"elapsedMs,omitempty"`
	RemainingMS *int64             `json:"remainingMs,omitempty"`
	Height      int                `json:"height,omitempty"`
	VCodec      string             `json:"vcodec,omitempty"`
	ACodec      string             `json:"acodec,omitempty"`
	AudioTracks int                `json:"audioTracks,omitempty"`
	Done        bool               `json:"done,omitempty"`
	Failed      bool               `json:"failed,omitempty"`
}

// prepare shows the screen's resolving step, or its failure; a screen that misses it shows nothing new, so its
// failure is no play's
func (s *Service) prepare(ctx context.Context, deviceID string, p preparing) {
	raw, err := json.Marshal(p)
	if err != nil {
		return
	}
	if _, err := s.d.Command(ctx, deviceID, CapVideo, "prepare", raw); err != nil {
		s.d.Log.Warn("video_prepare_failed", "device_id", deviceID, "state", p.State)
	}
}

type playURL struct {
	URL      string `json:"url"`
	Mode     string `json:"mode"`
	Metadata struct {
		Title      string `json:"title,omitempty"`
		Artist     string `json:"artist,omitempty"`
		ArtworkURL string `json:"artworkUrl,omitempty"`
	} `json:"metadata"`
}

func (s *Service) playAudio(ctx context.Context, deviceID string, p providers.Player, r Request) (PlayResult, error) {
	st, err := p.Resolve(ctx, r.VideoID)
	if err != nil {
		return PlayResult{}, err
	}
	u := st.URL
	if s.d.Origin != "" && s.d.Relay != nil {
		token, err := s.d.Relay.AddSet(deviceID, []streams.Upstream{{URL: st.URL}}, streams.Options{OnFirstByte: func() { ytdlp.RelayFirstByte(ctx) }})
		if err != nil {
			return PlayResult{}, errors.New(providers.CodePlayerFailed)
		}
		u = s.d.Origin + "/v2/stream/" + token
	}
	if len(u) > maxDeviceURL {
		return PlayResult{}, providers.ErrUnplayable
	}
	a := playURL{URL: u, Mode: "session"}
	m := metadataOf(r)
	a.Metadata.Title, a.Metadata.Artist, a.Metadata.ArtworkURL = m.Title, m.Channel, m.ArtworkURL
	raw, err := json.Marshal(a)
	if err != nil {
		return PlayResult{}, err
	}
	if _, err := s.d.Command(ctx, deviceID, CapURL, "play", raw); err != nil {
		return PlayResult{}, err
	}
	s.Forget(deviceID)
	return PlayResult{Mode: "audio"}, nil
}

// RenditionView is one rendition as the dashboard and the select_track command name it.
type RenditionView struct {
	ID       string `json:"id" doc:"The rendition's id for select_track, its height and fps: 720p, 1080p60."`
	Height   int    `json:"height"`
	FPS      int    `json:"fps,omitempty"`
	VCodec   string `json:"vcodec,omitempty" doc:"h264, vp9, av1 or hevc."`
	Bitrate  int    `json:"bitrate,omitempty" doc:"Bits a second, when known."`
	HDR      string `json:"hdr,omitempty" doc:"Its dynamic range when not SDR: HDR10, HLG, DV."`
	Selected bool   `json:"selected"`
}

// AudioView is one audio language of the video as the dashboard, the screen and a switch name it.
type AudioView struct {
	ID       string `json:"id" doc:"The language's id for a switch: a- and its language (a-ru), a-und for one without."`
	Language string `json:"language" doc:"BCP 47, short; und when YouTube names none."`
	Label    string `json:"label,omitempty" doc:"The language's name as YouTube gives it (Russian)."`
	Original bool   `json:"original,omitempty" doc:"The video's own language."`
	Dubbed   bool   `json:"dubbed,omitempty" doc:"A dubbing, YouTube's automatic ones among them."`
	Selected bool   `json:"selected,omitempty"`
}

// live is the device's session while its urls work; s.mu is held
func (s *Service) live(deviceID string) *session {
	sess := s.sessions[deviceID]
	if sess != nil && !sess.expires.IsZero() && !s.d.Now().Before(sess.expires) {
		delete(s.sessions, deviceID)
		return nil
	}
	return sess
}

// position is where the device's session is now, from its last report
func (s *Service) position(deviceID string) int64 {
	if s.d.Session == nil {
		return 0
	}
	m, ok := s.d.Session(deviceID)
	if !ok {
		return 0
	}
	// a day in ms bounds both, so the conversions below cannot overflow
	const bound = uint64(24 * time.Hour / time.Millisecond)
	pos := int64(min(m.PositionMs, bound)) //nolint:gosec // bounded above
	if m.State == "playing" && m.PositionAt != "" {
		if at, err := time.Parse(time.RFC3339Nano, m.PositionAt); err == nil {
			rate := float64(m.Rate)
			if rate <= 0 {
				rate = 1
			}
			pos += int64(float64(s.d.Now().Sub(at).Milliseconds()) * rate)
		}
	}
	if d := int64(min(m.DurationMs, bound)); m.DurationMs > 0 && pos > d { //nolint:gosec // bounded above
		pos = d
	}
	return max(0, pos)
}

// Forget drops the device's session (its deletion, or a play of something else)
func (s *Service) Forget(deviceID string) {
	s.mu.Lock()
	sess := s.sessions[deviceID]
	delete(s.sessions, deviceID)
	s.mu.Unlock()
	if sess != nil {
		s.dropToken(sess.token)
	}
}

// Intercept answers a select_track of a video rendition or an audio language the session holds itself, by playing
// it from where the device is; every other command goes to the device.
func (s *Service) Intercept(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, bool, error) {
	if capability != CapVideo || name != "select_track" {
		return nil, false, nil
	}
	var a struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if json.Unmarshal(args, &a) != nil || (a.Kind != "video" && a.Kind != "audio") {
		return nil, false, nil
	}
	s.mu.Lock()
	sess := s.live(deviceID)
	held := false
	if sess != nil && a.Kind == "video" {
		for _, rd := range sess.renditions {
			held = held || rd.ID == a.ID
		}
	}
	if sess != nil && a.Kind == "audio" {
		for _, at := range sess.audio {
			held = held || at.ID == a.ID
		}
	}
	s.mu.Unlock()
	if !held {
		return nil, false, nil
	}
	if a.Kind == "audio" {
		return nil, true, s.Switch(ctx, deviceID, Change{Audio: a.ID})
	}
	return nil, true, s.Switch(ctx, deviceID, Change{Rendition: a.ID})
}

// Code names a play's failure for a line or an answer
func Code(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrNotSetUp):
		return "not_set_up"
	case errors.Is(err, ErrNoScreen):
		return "no_screen"
	case errors.Is(err, ErrNoPlayer):
		return "no_player"
	case errors.Is(err, ErrNoSession):
		return "no_session"
	case errors.Is(err, ErrUnknownRendition):
		return "unknown_rendition"
	case errors.Is(err, ErrUnknownAudio):
		return "unknown_audio"
	case errors.Is(err, ErrUnknownView):
		return "unknown_view"
	case errors.Is(err, providers.ErrUnplayable):
		return "unplayable"
	case errors.Is(err, providers.ErrSignInRequired):
		return "sign_in_required"
	case err.Error() == providers.CodePlayerFailed:
		return providers.CodePlayerFailed
	}
	return "failed"
}

// Interceptor is a command interceptor (commands.Interceptor's shape)
type Interceptor = func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, bool, error)

// Chain consults each interceptor in turn; the first that handles a command answers it
func Chain(fs ...Interceptor) Interceptor {
	return func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, bool, error) {
		for _, f := range fs {
			if res, handled, err := f(ctx, deviceID, capability, name, args); handled {
				return res, true, err
			}
		}
		return nil, false, nil
	}
}
