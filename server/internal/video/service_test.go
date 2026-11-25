package video_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/streams"
	"git.shino.boo/devilreef/cadence/server/internal/video"
)

const vid = "5qZQEq_C3vc"

type sent struct {
	dev, capability, name string
	args                  map[string]any
}

// rig is the service over fakes: a player answering fixed renditions, devices whose display.screen state and
// reported commands are set per test, and a device that records every command
type rig struct {
	t     *testing.T
	svc   *video.Service
	relay *streams.Relay
	log   bytes.Buffer

	mu       sync.Mutex
	now      time.Time
	screens  map[string]string          // display.screen state per device
	reports  map[string]map[string]bool // capability/command per device
	sent     []sent
	profiles []catalog.DeviceProfile
	screen   ytdlp.Screen
	session  *media.MediaSession
	player   string
	lang     string
	plays    int
	// an older screen's schema: metadata beyond title, series and artworkUrl is refused
	oldScreen bool
	// resolveDelay is how long ResolveScreen takes, honouring its context
	resolveDelay time.Duration
}

type fakePlayer struct{ r *rig }

func (p fakePlayer) Resolve(_ context.Context, id string) (providers.Stream, error) {
	return providers.Stream{URL: "https://rr1.googlevideo.com/videoplayback?itag=140&id=" + id + "&sig=AUDIO-SECRET"}, nil
}

func (p fakePlayer) ResolveScreen(ctx context.Context, id string, prof catalog.DeviceProfile) (ytdlp.Screen, error) {
	if p.r.resolveDelay > 0 {
		select {
		case <-ctx.Done():
			return ytdlp.Screen{}, ctx.Err()
		case <-time.After(p.r.resolveDelay):
		}
	}
	p.r.mu.Lock()
	defer p.r.mu.Unlock()
	p.r.profiles = append(p.r.profiles, prof)
	if id != vid {
		return ytdlp.Screen{}, providers.ErrUnplayable
	}
	return p.r.screen, nil
}

func (p fakePlayer) SearchVideos(context.Context, string, int) ([]ytdlp.Video, error) {
	return nil, nil
}

func adaptive() ytdlp.Screen {
	audio := "https://rr1.googlevideo.com/videoplayback?itag=140&sig=AUDIO-SECRET"
	return ytdlp.Screen{Stream: catalog.Stream{
		URL: "https://rr1.googlevideo.com/videoplayback?itag=248&sig=V1080", AudioURL: audio, DurationMS: 7_200_000,
		Renditions: []catalog.Rendition{
			{Height: 1080, FPS: 30, VCodec: "vp9", URL: "https://rr1.googlevideo.com/videoplayback?itag=248&sig=V1080", AudioURL: audio, Bitrate: 2_500_000},
			{Height: 720, FPS: 60, VCodec: "h264", URL: "https://rr1.googlevideo.com/videoplayback?itag=298&sig=V72060", AudioURL: audio},
			{Height: 720, FPS: 30, VCodec: "h264", URL: "https://rr1.googlevideo.com/videoplayback?itag=136&sig=V720", AudioURL: audio},
		},
	}}
}

func newRig(t *testing.T) *rig {
	r := &rig{
		t: t, now: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC), player: "ytdlp", screen: adaptive(),
		screens: map[string]string{"tv": `{"power":"on","screen":true,"maxHeight":1080,"codecs":["vp9","h264"]}`},
		reports: map[string]map[string]bool{
			"tv":      {"media.video/play": true, "media.url/play": true},
			"speaker": {"media.url/play": true},
		},
	}
	r.relay = streams.New(streams.Config{Now: r.clock})
	r.svc = video.New(video.Deps{
		Settings: func(context.Context, string) (settings.Values, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return settings.Values{MusicPlayer: r.player, Language: r.lang}, nil
		},
		Player: func(_ context.Context, name string) (providers.Player, error) {
			if name != "ytdlp" {
				return nil, errors.New("no instance")
			}
			return fakePlayer{r}, nil
		},
		State: func(_ context.Context, dev, capability string) (json.RawMessage, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			s, ok := r.screens[dev]
			return json.RawMessage(s), ok && capability == video.CapScreen
		},
		Reports: func(_ context.Context, dev, capability, command string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if !r.reports[dev][capability+"/"+command] {
				return devices.ErrUnknownCapability
			}
			return nil
		},
		Relay: r.relay, Origin: "https://cadence.example",
		Command: func(_ context.Context, dev, capability, name string, args []byte) (json.RawMessage, error) {
			var a map[string]any
			if err := json.Unmarshal(args, &a); err != nil {
				t.Fatalf("args %s", args)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			r.sent = append(r.sent, sent{dev, capability, name, a})
			if md, ok := a["metadata"].(map[string]any); ok && r.oldScreen && (md["channel"] != nil || md["provider"] != nil) {
				return nil, &commands.InvalidArgumentsError{Path: "/metadata"}
			}
			if name == "play" {
				r.plays++
				return json.RawMessage(`{"sessionId":"video-` + strconv.Itoa(r.plays) + `"}`), nil
			}
			return nil, nil
		},
		Session: func(string) (media.MediaSession, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.session == nil {
				return media.MediaSession{}, false
			}
			return *r.session, true
		},
		Log: slog.New(slog.NewTextHandler(&r.log, nil)), Now: r.clock,
		Image: func(raw string) string { return "https://cadence.example/v2/images/" + strconv.Itoa(len(raw)) },
	})
	return r
}

func (r *rig) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *rig) last() sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sent) == 0 {
		r.t.Fatal("nothing sent")
	}
	return r.sent[len(r.sent)-1]
}

func (r *rig) noLeak() {
	r.t.Helper()
	for _, leak := range []string{"googlevideo", "SECRET", "sig=", "Title", vid, "cadence.example"} {
		if strings.Contains(r.log.String(), leak) {
			r.t.Fatalf("log carries %q: %s", leak, r.log.String())
		}
	}
}

func TestPlayOnAScreenSendsTheTopRenditionThroughTheRelay(t *testing.T) {
	r := newRig(t)
	res, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid, StartMS: 5000, Title: "A Title", Channel: "Chan", ArtworkURL: "https://i.ytimg.com/vi/x/hq.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if res != (video.PlayResult{Mode: "video", Height: 1080, Renditions: 3, View: "video", Title: "A Title"}) {
		t.Fatalf("result %+v", res)
	}
	if p := r.profiles[0]; !p.Screen || p.MaxHeight != 1080 || strings.Join(p.Codecs, ",") != "vp9,h264" {
		t.Fatalf("profile %+v", p)
	}
	s := r.last()
	if s.dev != "tv" || s.capability != "media.video" || s.name != "play" || len(r.sent) != 1 {
		t.Fatalf("sent %+v", r.sent)
	}
	url, audio := s.args["url"].(string), s.args["audioUrl"].(string)
	if !strings.HasPrefix(url, "https://cadence.example/v2/stream/") || !strings.HasSuffix(url, "/1") || !strings.HasSuffix(audio, "/0") ||
		strings.TrimSuffix(url, "1") != strings.TrimSuffix(audio, "0") {
		t.Fatalf("urls %q %q", url, audio)
	}
	md := s.args["metadata"].(map[string]any)
	if s.args["startMs"].(float64) != 5000 || md["title"] != "A Title" || md["channel"] != "Chan" || md["series"] != nil ||
		md["provider"] != "youtube" || md["providerName"] != "YouTube" || md["artworkUrl"] == nil {
		t.Fatalf("args %+v", s.args)
	}
	for k := range s.args {
		if k == "volume" || k == "level" {
			t.Fatal("a play carries a volume")
		}
	}
	token := strings.Split(strings.TrimPrefix(url, "https://cadence.example/v2/stream/"), "/")[0]
	if exp, ok := r.relay.Expires(token); !ok || exp.Sub(r.now) < 5*time.Hour {
		t.Fatalf("a two-hour film's token: %v", exp)
	}
	views, ok := r.svc.Renditions("tv")
	if !ok || len(views) != 3 || views[0].ID != "1080p" || !views[0].Selected || views[1].ID != "720p60" || views[2].ID != "720p30" {
		t.Fatalf("renditions %+v", views)
	}
	// the screen gets the same list, by name and never a url, so it can offer the qualities itself
	listed := s.args["renditions"].([]any)
	first := listed[0].(map[string]any)
	if len(listed) != 3 || first["name"] != "1080p" || first["height"].(float64) != 1080 || first["selected"] != true ||
		listed[1].(map[string]any)["name"] != "720p60" || listed[1].(map[string]any)["selected"] != nil {
		t.Fatalf("listed %+v", listed)
	}
	for _, x := range listed {
		for k := range x.(map[string]any) {
			if k == "url" || k == "audioUrl" {
				t.Fatalf("a listed rendition carries %s", k)
			}
		}
	}
	r.noLeak()
}

func TestSelectTrackOfARenditionPlaysItFromWhereTheScreenIs(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid}); err != nil {
		t.Fatal(err)
	}
	r.session = &media.MediaSession{State: "playing", PositionMs: 60_000, PositionAt: r.now.Add(-2 * time.Second).Format(time.RFC3339Nano), Rate: 1}
	_, handled, err := r.svc.Intercept(context.Background(), "tv", "media.video", "select_track", []byte(`{"kind":"video","id":"720p30"}`))
	if !handled || err != nil {
		t.Fatalf("handled %v err %v", handled, err)
	}
	s := r.last()
	if s.name != "play" || !strings.HasSuffix(s.args["url"].(string), "/3") || s.args["startMs"].(float64) != 62_000 {
		t.Fatalf("sent %+v", s)
	}
	views, _ := r.svc.Renditions("tv")
	if views[0].Selected || !views[2].Selected {
		t.Fatalf("views %+v", views)
	}
	if l := s.args["renditions"].([]any); l[0].(map[string]any)["selected"] != nil || l[2].(map[string]any)["selected"] != true {
		t.Fatalf("listed %+v", l)
	}
	// the device's own tracks, and an id the session does not hold, go to the device
	for _, args := range []string{`{"kind":"audio","id":"1"}`, `{"kind":"video","id":"4320p"}`} {
		if _, handled, _ := r.svc.Intercept(context.Background(), "tv", "media.video", "select_track", []byte(args)); handled {
			t.Fatalf("%s handled", args)
		}
	}
	if _, handled, _ := r.svc.Intercept(context.Background(), "speaker", "media.video", "select_track", []byte(`{"kind":"video","id":"720p30"}`)); handled {
		t.Fatal("another device's select handled")
	}
}

func TestALiveScreenPlaysItsHLSVariantFromNow(t *testing.T) {
	r := newRig(t)
	r.screen = ytdlp.Screen{HLS: true, Stream: catalog.Stream{Live: true, Renditions: []catalog.Rendition{
		{Height: 720, FPS: 30, VCodec: "h264", ACodec: "mp4a.40.2", URL: "https://manifest.googlevideo.com/api/manifest/hls_playlist/itag/95/index.m3u8"},
	}}}
	res, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid, StartMS: 9000})
	if err != nil || !res.Live {
		t.Fatalf("%+v %v", res, err)
	}
	s := r.last()
	if s.args["live"] != true || s.args["startMs"] != nil || s.args["audioUrl"] != nil || !strings.HasSuffix(s.args["url"].(string), "/0") {
		t.Fatalf("args %+v", s.args)
	}
}

func TestPlayOnASpeakerSendsTheAudio(t *testing.T) {
	r := newRig(t)
	res, err := r.svc.Play(context.Background(), "speaker", video.Request{VideoID: vid, Title: "T"})
	if err != nil || res.Mode != "audio" {
		t.Fatalf("%+v %v", res, err)
	}
	s := r.last()
	if s.capability != "media.url" || s.args["mode"] != "session" || !strings.HasPrefix(s.args["url"].(string), "https://cadence.example/v2/stream/") {
		t.Fatalf("sent %+v", s)
	}
	if len(r.profiles) != 0 {
		t.Fatal("a speaker resolved renditions")
	}
	if _, err := r.svc.Play(context.Background(), "speaker", video.Request{VideoID: vid, ScreenOnly: true}); !errors.Is(err, video.ErrNoScreen) {
		t.Fatalf("screen only: %v", err)
	}
	r.noLeak()
}

func TestAScreenStateWithoutMediaVideoIsNoScreen(t *testing.T) {
	r := newRig(t)
	r.reports["tv"] = map[string]bool{}
	if p := r.svc.Profile(context.Background(), "tv"); p.Screen {
		t.Fatal("a screen without media.video")
	}
	if _, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid}); !errors.Is(err, video.ErrNoPlayer) {
		t.Fatalf("err %v", err)
	}
	r.screens["tv"] = `{"power":"on","screen":false}`
	r.reports["tv"] = map[string]bool{"media.video/play": true}
	if p := r.svc.Profile(context.Background(), "tv"); p.Screen {
		t.Fatal("screen: false")
	}
}

func TestPlayWithoutAPlayerIsNotSetUp(t *testing.T) {
	r := newRig(t)
	r.player = ""
	if _, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid}); !errors.Is(err, video.ErrNotSetUp) {
		t.Fatalf("err %v", err)
	}
	if video.Code(providers.ErrSignInRequired) != "sign_in_required" {
		t.Fatal("code")
	}
}

func TestRenditionsExpireWithTheirToken(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid}); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.now = r.now.Add(25 * time.Hour)
	r.mu.Unlock()
	if _, ok := r.svc.Renditions("tv"); ok {
		t.Fatal("renditions kept past their token")
	}
	if err := r.svc.SelectRendition(context.Background(), "tv", "720p30"); !errors.Is(err, video.ErrNoSession) {
		t.Fatalf("err %v", err)
	}
}

func TestChainAsksEachInterceptorInTurn(t *testing.T) {
	var asked []string
	f := func(name string, handles bool) video.Interceptor {
		return func(context.Context, string, string, string, []byte) (json.RawMessage, bool, error) {
			asked = append(asked, name)
			return nil, handles, nil
		}
	}
	_, handled, _ := video.Chain(f("a", false), f("b", true), f("c", true))(context.Background(), "d", "c", "n", nil)
	if !handled || strings.Join(asked, "") != "ab" {
		t.Fatalf("%v %v", handled, asked)
	}
}

// a screen from before the channel and the provider refuses them by its schema: the play goes again with what it knows
func TestPlayOnAnOlderScreenSendsTheMetadataItKnows(t *testing.T) {
	r := newRig(t)
	r.oldScreen = true
	if _, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid, Title: "A Title", Channel: "Chan"}); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 2 {
		t.Fatalf("sent %+v", r.sent)
	}
	md := r.last().args["metadata"].(map[string]any)
	if md["title"] != "A Title" || md["channel"] != nil || md["provider"] != nil {
		t.Fatalf("metadata %+v", md)
	}
}

// a turn's deadline (a fast mode's step) must not cancel the screen's play midway: the screen would show a failure
// for a play that works
func TestAScreenPlayOutlivesItsCallersDeadline(t *testing.T) {
	r := newRig(t)
	r.resolveDelay = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := r.svc.Play(ctx, "tv", video.Request{VideoID: vid, Title: "A Title"}); err != nil {
		t.Fatal(err)
	}
	if s := r.last(); s.name != "play" || s.args["url"] == nil {
		t.Fatalf("sent %+v", r.sent)
	}
}
