package ytdlp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveScreenPicksOneRenditionPerHeightAndOneAudio(t *testing.T) {
	bin, dir := fakeBin(t, "cat '"+fixture(t, "screen.json")+"'")
	var log bytes.Buffer
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&log)})
	s, err := p.ResolveScreen(context.Background(), "5qZQEq_C3vc", catalog.DeviceProfile{Screen: true, MaxHeight: 1080, Codecs: []string{"vp9", "h264"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range s.Renditions {
		got = append(got, r.VCodec+"@"+strconv.Itoa(r.Height))
		if !strings.Contains(r.AudioURL, "itag=140&") || r.ACodec != "mp4a.40.2" {
			t.Errorf("rendition %d audio %q %q", r.Height, r.AudioURL, r.ACodec)
		}
	}
	// 2160 is past maxHeight, av1 and the muxed 18 are left out, vp9 is preferred at 1080
	if strings.Join(got, ",") != "vp9@1080,h264@720,h264@360" {
		t.Fatalf("renditions %v", got)
	}
	if s.HLS || s.Live || s.DurationMS != 212400 || !strings.Contains(s.URL, "itag=248") || s.AudioURL != s.Renditions[0].AudioURL {
		t.Fatalf("stream %+v", s.Stream)
	}
	if s.Expires.Unix() != 1800000000 {
		t.Fatalf("expires %v", s.Expires)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	for _, want := range []string{"--print\n%(.{id,title,channel,uploader,duration,live_status,thumbnail,description,categories,track,artist,album,formats})j\n", "--\nhttps://www.youtube.com/watch?v=5qZQEq_C3vc\n"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	for _, leak := range []string{"googlevideo", "SECRET"} {
		if strings.Contains(log.String(), leak) {
			t.Fatalf("log carries %q: %s", leak, log.String())
		}
	}
}

func TestResolveScreenDefaultsToH264At1080(t *testing.T) {
	bin, _ := fakeBin(t, "cat '"+fixture(t, "screen.json")+"'")
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir()})
	s, err := p.ResolveScreen(context.Background(), "5qZQEq_C3vc", catalog.DeviceProfile{Screen: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Renditions) != 3 || s.Renditions[0].VCodec != "h264" || s.Renditions[0].Height != 1080 {
		t.Fatalf("renditions %+v", s.Renditions)
	}
}

func TestResolveScreenGivesALiveVideoItsHLSVariants(t *testing.T) {
	bin, _ := fakeBin(t, "cat '"+fixture(t, "live.json")+"'")
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir()})
	s, err := p.ResolveScreen(context.Background(), "5qZQEq_C3vc", catalog.DeviceProfile{Screen: true, MaxHeight: 720, Codecs: []string{"h264"}})
	if err != nil {
		t.Fatal(err)
	}
	if !s.HLS || !s.Live || s.DurationMS != 0 || s.AudioURL != "" || len(s.Renditions) != 2 {
		t.Fatalf("stream %+v", s)
	}
	if r := s.Renditions[0]; r.Height != 720 || r.FPS != 60 || !strings.Contains(r.URL, "/itag/300/") || r.ACodec == "" {
		t.Fatalf("first %+v", r)
	}
	if s.Expires.Unix() != 1900000000 {
		t.Fatalf("expires %v", s.Expires)
	}
}

func TestResolveScreenRefusesWhatTheScreenCannotDecode(t *testing.T) {
	bin, _ := fakeBin(t, "cat '"+fixture(t, "live.json")+"'")
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir()})
	_, err := p.ResolveScreen(context.Background(), "5qZQEq_C3vc", catalog.DeviceProfile{Screen: true, Codecs: []string{"av1"}})
	if !errors.Is(err, providers.ErrUnplayable) {
		t.Fatalf("err %v", err)
	}
	if _, err := p.ResolveScreen(context.Background(), "../etc", catalog.DeviceProfile{}); !errors.Is(err, providers.ErrUnplayable) {
		t.Fatalf("bad id: %v", err)
	}
}

func TestResolveScreenNamesBadOutput(t *testing.T) {
	bin, _ := fakeBin(t, "echo 'not json LEAK'")
	var log bytes.Buffer
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&log)})
	_, err := p.ResolveScreen(context.Background(), "5qZQEq_C3vc", catalog.DeviceProfile{})
	if err == nil || err.Error() != providers.CodePlayerFailed || strings.Contains(log.String(), "LEAK") {
		t.Fatalf("err %v log %s", err, log.String())
	}
}

func TestSearchVideosReadsEachEntry(t *testing.T) {
	bin, dir := fakeBin(t, "cat '"+fixture(t, "search.jsonl")+"'")
	var log bytes.Buffer
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&log)})
	vs, err := p.SearchVideos(context.Background(), "  котики   QUERY ", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].Title != "First video" || vs[0].DurationS != 62 || vs[0].Channel != "Chan" ||
		vs[1].Channel != "Up" || !vs[1].Live || vs[1].ID != "dQw4w9WgXcQ" {
		t.Fatalf("videos %+v", vs)
	}
	if vs[0].ThumbnailURL() != "https://i.ytimg.com/vi/5qZQEq_C3vc/hqdefault.jpg" {
		t.Fatal(vs[0].ThumbnailURL())
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(args), "--\nytsearch5:котики QUERY\n") || !strings.Contains(string(args), "--flat-playlist") {
		t.Fatalf("args %s", args)
	}
	if strings.Contains(log.String(), "QUERY") {
		t.Fatal("log carries the query")
	}
}

func TestResolveScreenReadsTheVideoAndItsAudioLanguages(t *testing.T) {
	bin, dir := fakeBin(t, "cat '"+fixture(t, "multiaudio.json")+"'")
	var log bytes.Buffer
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&log)})
	s, err := p.ResolveScreen(context.Background(), "nK8liKE8YBk", catalog.DeviceProfile{Screen: true, MaxHeight: 2160, Codecs: []string{"vp9", "h264"}})
	if err != nil {
		t.Fatal(err)
	}
	in := s.Info
	if in.Title != "A Song (Official Video)" || in.Channel != "Artist - Topic" || !in.Music || in.Artist != "Artist" ||
		in.Track != "A Song" || in.Album != "An Album" || in.DurationMS != 245500 ||
		in.ThumbnailURL != "https://i.ytimg.com/vi/nK8liKE8YBk/maxresdefault.jpg" || !strings.HasPrefix(in.Description, "First line") {
		t.Fatalf("info %+v", in)
	}
	var langs []string
	for _, a := range s.Audio {
		langs = append(langs, a.ID+"/"+a.Label+"/"+strconv.FormatBool(a.Original)+"/"+strconv.FormatBool(a.Dubbed)+"/"+a.ACodec)
	}
	// the original first; ru's best is the m4a over the opus, by bitrate
	if strings.Join(langs, ",") != "a-en/English/true/false/mp4a.40.2,a-es/Spanish/false/false/mp4a.40.2,a-ru/Russian/false/true/mp4a.40.2" {
		t.Fatalf("audio %v", langs)
	}
	if !strings.Contains(s.AudioURL, "EN-SECRET") {
		t.Fatal("the renditions' audio is not the original")
	}
	var hdr []string
	for i, r := range s.Renditions {
		hdr = append(hdr, strconv.Itoa(r.Height)+":"+s.HDR[i])
	}
	if strings.Join(hdr, ",") != "2160:HDR10,1080:HDR10,720:" {
		t.Fatalf("hdr %v", hdr)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(args), "categories,track,artist,album,formats})j") {
		t.Fatalf("args %s", args)
	}
	for _, leak := range []string{"SECRET", "A Song", "description", "Artist"} {
		if strings.Contains(log.String(), leak) {
			t.Fatalf("log carries %q", leak)
		}
	}
}

func TestResolveScreenFallsBackToTheVideosOwnThumbnail(t *testing.T) {
	bin, _ := fakeBin(t, "cat '"+fixture(t, "screen.json")+"'")
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir()})
	s, err := p.ResolveScreen(context.Background(), "5qZQEq_C3vc", catalog.DeviceProfile{Screen: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.Info.ThumbnailURL != "https://i.ytimg.com/vi/5qZQEq_C3vc/hqdefault.jpg" || s.Info.Music || len(s.Audio) != 1 || s.Audio[0].ID != "a-und" || s.HDR != nil {
		t.Fatalf("screen %+v %+v", s.Info, s.Audio)
	}
}

// youtube serves a format's url only from its available_at: a fetch before it answers 403, so the resolve waits
func TestResolveScreenWaitsUntilYouTubeServesTheFormats(t *testing.T) {
	raw, err := os.ReadFile(fixture(t, "screen.json"))
	if err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	const availableAt = 1800000000.0
	for _, f := range in["formats"].([]any) {
		f.(map[string]any)["available_at"] = availableAt
	}
	out, _ := json.Marshal(in)
	path := filepath.Join(t.TempDir(), "available.json")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	bin, _ := fakeBin(t, "cat '"+path+"'")
	prof := catalog.DeviceProfile{Screen: true, MaxHeight: 1080, Codecs: []string{"h264"}}
	for _, c := range []struct {
		name     string
		ahead    time.Duration
		min, max time.Duration
	}{
		{"before available_at it waits", 300 * time.Millisecond, 250 * time.Millisecond, 3 * time.Second},
		{"after it, at once", -time.Second, 0, 250 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			var log bytes.Buffer
			now := time.Unix(availableAt, 0).Add(-c.ahead)
			p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&log), Now: func() time.Time { return now }})
			var events []ytdlp.ResolveEvent
			ctx := ytdlp.WithProgress(context.Background(), func(ev ytdlp.ResolveEvent) {
				if ev.Stage == playprogress.StageWaitAvailable && !ev.Done && !ev.Failed {
					events = append(events, ev)
				}
			})
			start := time.Now()
			if _, err := p.ResolveScreen(ctx, "5qZQEq_C3vc", prof); err != nil {
				t.Fatal(err)
			}
			if c.ahead > 0 && (len(events) != 2 || events[0].RemainingMS <= 0 || events[len(events)-1].RemainingMS != 0) {
				t.Fatalf("countdown %+v", events)
			}
			if c.ahead <= 0 && len(events) != 0 {
				t.Fatalf("waited after available_at: %+v", events)
			}
			if took := time.Since(start); took < c.min || took > c.max {
				t.Fatalf("took %v, want %v..%v", took, c.min, c.max)
			}
		})
	}
	t.Run("a cancelled resolve stops waiting", func(t *testing.T) {
		now := time.Unix(availableAt, 0).Add(-time.Hour)
		p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&bytes.Buffer{}), Now: func() time.Time { return now }})
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, err := p.ResolveScreen(ctx, "5qZQEq_C3vc", prof); err == nil {
			t.Fatal("resolved past its context")
		}
	})
}
