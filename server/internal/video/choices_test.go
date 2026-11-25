package video_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/video"
)

// a music video with three audio languages, as yt-dlp reads it
func musicVideo() ytdlp.Screen {
	s := adaptive()
	s.Info = ytdlp.Info{
		Title: "A Song (Official Video)", Channel: "Artist - Topic", Description: "About the song.", DurationMS: 245_000,
		ThumbnailURL: "https://i.ytimg.com/vi/" + vid + "/maxresdefault.jpg", Music: true, Track: "A Song", Album: "LP",
	}
	s.Audio = []ytdlp.AudioTrack{
		{ID: "a-en", Language: "en", Label: "English", Original: true, ACodec: "mp4a.40.2", URL: "https://rr1.googlevideo.com/videoplayback?itag=140&lang=en&sig=EN-SECRET"},
		{ID: "a-es", Language: "es", Label: "Spanish", Dubbed: true, URL: "https://rr1.googlevideo.com/videoplayback?itag=140&lang=es&sig=ES-SECRET"},
		{ID: "a-ru", Language: "ru", Label: "Russian", Dubbed: true, URL: "https://rr1.googlevideo.com/videoplayback?itag=140&lang=ru&sig=RU-SECRET"},
	}
	s.HDR = []string{"HDR10", "", ""}
	return s
}

// the relay's upstream index a stream url names
func index(u string) string { return u[strings.LastIndex(u, "/")+1:] }

func (r *rig) named(name string) []sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []sent
	for _, s := range r.sent {
		if s.name == name {
			out = append(out, s)
		}
	}
	return out
}

// the screen shows its resolving step before yt-dlp runs, named once the video is known; the play carries the
// video's own title, channel and still (through the image proxy), the device's language's audio, and the choices
func TestPlayShowsTheResolvingStepAndTheVideosOwnMetadata(t *testing.T) {
	r := newRig(t)
	r.screen = musicVideo()
	r.screen.Info.Music = false
	r.lang = "ru-RU"
	r.reports["tv"]["media.video/prepare"] = true
	res, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid})
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "A Song (Official Video)" || res.View != "video" || res.Height != 1080 {
		t.Fatalf("result %+v", res)
	}
	r.mu.Lock()
	order := []string{}
	for _, s := range r.sent {
		order = append(order, s.name+":"+func() string { st, _ := s.args["state"].(string); return st }())
	}
	r.mu.Unlock()
	if strings.Join(order, ",") != "prepare:resolving,prepare:resolving,play:" {
		t.Fatalf("sent %v", order)
	}
	first, named := r.named("prepare")[0].args, r.named("prepare")[1].args
	// before yt-dlp: the still the video id names, through the proxy
	if first["artworkUrl"] != "https://cadence.example/v2/images/"+itoa(len("https://i.ytimg.com/vi/"+vid+"/hqdefault.jpg")) || first["title"] != nil {
		t.Fatalf("first prepare %+v", first)
	}
	if named["title"] != "A Song (Official Video)" || named["subtitle"] != "Artist - Topic" {
		t.Fatalf("named prepare %+v", named)
	}
	play := r.last().args
	md := play["metadata"].(map[string]any)
	if md["title"] != "A Song (Official Video)" || md["channel"] != "Artist - Topic" || md["kind"] != nil ||
		md["artworkUrl"] != "https://cadence.example/v2/images/"+itoa(len(r.screen.Info.ThumbnailURL)) {
		t.Fatalf("metadata %+v", md)
	}
	// ru is kept first and plays; the original after it
	if index(play["audioUrl"].(string)) != "0" || play["view"] != nil || play["views"] != nil {
		t.Fatalf("play %+v", play)
	}
	choices := play["audioChoices"].([]any)
	if len(choices) != 3 {
		t.Fatalf("choices %+v", choices)
	}
	ru, en := choices[0].(map[string]any), choices[1].(map[string]any)
	if ru["id"] != "a-ru" || ru["selected"] != true || ru["dubbed"] != true || en["id"] != "a-en" || en["original"] != true || en["selected"] != nil {
		t.Fatalf("choices %+v", choices)
	}
	for _, c := range choices {
		for k := range c.(map[string]any) {
			if k == "url" {
				t.Fatal("a choice carries its url")
			}
		}
	}
	if l := play["renditions"].([]any); l[0].(map[string]any)["hdr"] != "HDR10" || l[1].(map[string]any)["hdr"] != nil {
		t.Fatalf("renditions %+v", l)
	}
	v, ok := r.svc.View("tv")
	if !ok || v.SessionID != "video-1" || v.Description != "About the song." || v.DurationMS != 245_000 || len(v.Audio) != 3 ||
		!v.Audio[0].Selected || v.Audio[0].ID != "a-ru" || len(v.Views) != 0 || v.Renditions[0].HDR != "HDR10" {
		t.Fatalf("view %+v", v)
	}
	r.noLeak()
	for _, leak := range []string{"Song", "Artist", "About"} {
		if strings.Contains(r.log.String(), leak) {
			t.Fatalf("log carries %q", leak)
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// without a language of the device's, the original plays
func TestTheOriginalAudioPlaysWithoutTheDevicesLanguage(t *testing.T) {
	r := newRig(t)
	r.screen = musicVideo()
	r.lang = "de"
	if _, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid}); err != nil {
		t.Fatal(err)
	}
	v, _ := r.svc.View("tv")
	if v.Audio[0].ID != "a-en" || !v.Audio[0].Selected {
		t.Fatalf("audio %+v", v.Audio)
	}
}

// a music.youtube.com link plays its audio alone with the visualizer, as a track; the views switch from where it
// plays, and a quality picked in an audio view goes back to the picture
func TestAMusicLinkPlaysItsAudioWithTheVisualizerAndSwitchesViews(t *testing.T) {
	r := newRig(t)
	r.screen = musicVideo()
	if _, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid, Music: true}); err != nil {
		t.Fatal(err)
	}
	play := r.last().args
	md := play["metadata"].(map[string]any)
	if play["view"] != "visualizer" || len(play["views"].([]any)) != 3 || play["audioUrl"] != nil || play["renditions"] != nil ||
		index(play["url"].(string)) != "0" || md["kind"] != "track" || md["title"] != "A Song" || md["artist"] != "Artist" ||
		md["album"] != "LP" || md["provider"] != "youtube_music" {
		t.Fatalf("play %+v", play)
	}
	r.session = &media.MediaSession{SessionID: "video-1", State: "playing", PositionMs: 30_000, PositionAt: r.now.Format(time.RFC3339Nano), Rate: 1}
	if err := r.svc.Switch(context.Background(), "tv", video.Change{View: "video"}); err != nil {
		t.Fatal(err)
	}
	play = r.last().args
	if play["view"] != "video" || index(play["url"].(string)) != "3" || index(play["audioUrl"].(string)) != "0" ||
		play["startMs"].(float64) != 30_000 || play["metadata"].(map[string]any)["kind"] != nil {
		t.Fatalf("video view %+v", play)
	}
	if err := r.svc.Switch(context.Background(), "tv", video.Change{View: "audio", Audio: "a-ru"}); err != nil {
		t.Fatal(err)
	}
	play = r.last().args
	if play["view"] != "audio" || index(play["url"].(string)) != "2" {
		t.Fatalf("audio view %+v", play)
	}
	if err := r.svc.Switch(context.Background(), "tv", video.Change{Rendition: "720p30"}); err != nil {
		t.Fatal(err)
	}
	v, _ := r.svc.View("tv")
	if v.View != "video" || !v.Renditions[2].Selected || !v.Audio[2].Selected || v.SessionID != "video-4" {
		t.Fatalf("view %+v", v)
	}
	for _, c := range []video.Change{{View: "radio"}, {Audio: "a-de"}, {Rendition: "4320p"}} {
		if err := r.svc.Switch(context.Background(), "tv", c); err == nil {
			t.Fatalf("%+v switched", c)
		}
	}
	// a select_track of an audio language the session holds is the server's; another goes to the device
	if _, handled, err := r.svc.Intercept(context.Background(), "tv", "media.video", "select_track", []byte(`{"kind":"audio","id":"a-es"}`)); !handled || err != nil {
		t.Fatalf("handled %v %v", handled, err)
	}
	if _, handled, _ := r.svc.Intercept(context.Background(), "tv", "media.video", "select_track", []byte(`{"kind":"audio","id":"1"}`)); handled {
		t.Fatal("an mpv track handled")
	}
}

// a video YouTube does not file as music has no views
func TestAVideoThatIsNotMusicHasNoViews(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid, View: "audio"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.svc.View("tv"); v.View != "video" || len(v.Views) != 0 || len(v.Audio) != 0 {
		t.Fatalf("view %+v", v)
	}
	if err := r.svc.Switch(context.Background(), "tv", video.Change{View: "audio"}); err == nil {
		t.Fatal("switched to a view it has not")
	}
}

// a failed resolve shows its failure on the screen
func TestAFailedResolveShowsItsFailure(t *testing.T) {
	r := newRig(t)
	r.reports["tv"]["media.video/prepare"] = true
	_, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: "AAAAAAAAAAA"})
	if err == nil {
		t.Fatal("played")
	}
	p := r.named("prepare")
	if len(p) != 2 || p[1].args["state"] != "failed" || p[1].args["code"] != video.Code(providers.ErrUnplayable) || len(r.named("play")) != 0 {
		t.Fatalf("prepares %+v", p)
	}
}

// the screen's own switch: its session alone, a choice it holds
func TestTheScreensSwitchIsItsSessionsAlone(t *testing.T) {
	r := newRig(t)
	r.screen = musicVideo()
	if _, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid}); err != nil {
		t.Fatal(err)
	}
	method := r.svc.SwitchMethod()
	call := func(dev, args string) calls.Reply {
		return method(context.Background(), calls.Call{DeviceID: dev, Method: rpc.MediaVideo, Args: json.RawMessage(args)})
	}
	if rep := call("tv", `{"sessionId":"video-1","audio":"a-es"}`); rep.Status != rpc.StatusOK {
		t.Fatalf("reply %+v", rep)
	}
	if index(r.last().args["audioUrl"].(string)) != "1" {
		t.Fatalf("play %+v", r.last().args)
	}
	for args, want := range map[string]string{
		`{"sessionId":"video-1"}`:                     rpc.StatusInvalid,
		`{"sessionId":"video-9","quality":"720p30"}`:  rpc.StatusNotFound,
		`{"sessionId":"video-2","quality":"4320p"}`:   rpc.StatusInvalid,
		`{"sessionId":"video-2","view":"radio"}`:      rpc.StatusInvalid,
		`{"sessionId":"video-2","quality":1}`:         rpc.StatusInvalid,
		`{"sessionId":"video-2","url":"https://x/y"}`: rpc.StatusInvalid,
	} {
		if rep := call("tv", args); rep.Status != want {
			t.Errorf("%s: %+v, want %s", args, rep, want)
		}
	}
	if rep := call("speaker", `{"sessionId":"video-2","quality":"720p30"}`); rep.Status != rpc.StatusNotFound {
		t.Fatalf("another device: %+v", rep)
	}
}

func TestAdminSwitchesThePlayingVideo(t *testing.T) {
	r := newRig(t)
	r.screen = musicVideo()
	cfg := huma.DefaultConfig("t", "1")
	cfg.CreateHooks = nil
	_, a := humatest.New(t, cfg)
	video.RegisterAdmin(a, r.svc, devs{})
	if resp := a.Patch("/devices/tv/video", map[string]any{"rendition": "720p30"}); resp.Code != http.StatusNotFound ||
		!strings.Contains(resp.Body.String(), "no_session") {
		t.Fatalf("%d %s", resp.Code, resp.Body)
	}
	resp := a.Post("/devices/tv/video", map[string]any{"url": "https://music.youtube.com/watch?v=" + vid, "view": "audio"})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"view":"audio"`) {
		t.Fatalf("%d %s", resp.Code, resp.Body)
	}
	resp = a.Patch("/devices/tv/video", map[string]any{"view": "video", "audio": "a-ru"})
	if resp.Code != http.StatusOK {
		t.Fatalf("%d %s", resp.Code, resp.Body)
	}
	var v video.VideoView
	_ = json.Unmarshal(resp.Body.Bytes(), &v)
	if v.View != "video" || v.Title != "A Song" || !v.Music || len(v.Views) != 3 || v.SessionID != "video-2" {
		t.Fatalf("view %+v", v)
	}
	for body, want := range map[string]string{`{}`: "nothing_to_change", `{"audio":"a-de"}`: "unknown_audio", `{"rendition":"4320p"}`: "unknown_rendition"} {
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		if resp := a.Patch("/devices/tv/video", m); resp.Code != http.StatusUnprocessableEntity || !strings.Contains(resp.Body.String(), want) {
			t.Errorf("%s: %d %s", body, resp.Code, resp.Body)
		}
	}
	if resp := a.Get("/devices/tv/video"); !strings.Contains(resp.Body.String(), `"description":"About the song."`) {
		t.Fatalf("%s", resp.Body)
	}
	r.noLeak()
}

// a song plays with a square cover: the music catalog's own when the request brought one, else the video's frame
// without its letterbox, which the screen's square box crops to its centre; a plain video keeps its still
func TestASongsCoverIsSquareArt(t *testing.T) {
	cover := "https://lh3.googleusercontent.com/cover=w544-h544-l90-rj"
	for _, c := range []struct {
		req   video.Request
		thumb string
		want  string
	}{
		{video.Request{VideoID: vid, Music: true, ArtworkURL: cover}, "https://i.ytimg.com/vi/" + vid + "/maxresdefault.jpg", cover},
		// the fake proxy names an image by its url's length: 0.jpg and mqdefault.jpg tell apart
		{video.Request{VideoID: vid, Music: true}, "https://i.ytimg.com/vi/" + vid + "/0.jpg", "https://i.ytimg.com/vi/" + vid + "/mqdefault.jpg"},
		{video.Request{VideoID: vid, ArtworkURL: cover}, "https://i.ytimg.com/vi/" + vid + "/0.jpg", "https://i.ytimg.com/vi/" + vid + "/0.jpg"},
	} {
		r := newRig(t)
		r.screen = musicVideo()
		r.screen.Info.Music = c.req.Music
		r.screen.Info.ThumbnailURL = c.thumb
		if _, err := r.svc.Play(context.Background(), "tv", c.req); err != nil {
			t.Fatal(err)
		}
		md := r.last().args["metadata"].(map[string]any)
		if md["artworkUrl"] != "https://cadence.example/v2/images/"+itoa(len(c.want)) {
			t.Fatalf("%+v: artwork %v, want the proxy's of %s", c.req, md["artworkUrl"], c.want)
		}
	}
}
