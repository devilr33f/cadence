package ytdlp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytmusic"
)

const (
	// a video's info with every format, captions left out by the template
	maxScreenOutput = 8 << 20
	// the height a screen that names none gets at most
	defaultMaxHeight = 1080
	// at most this many renditions per video
	maxRenditions = 8
	// longest url a rendition keeps: the relay fetches it, the device never sees it
	maxUpstreamURL = 8 << 10
	// the fields of a video the screen mode reads, as one JSON object
	screenTemplate = "%(.{id,title,channel,uploader,duration,live_status,thumbnail,description,categories,track,artist,album,formats})j"
	searchTemplate = "%(.{id,title,channel,uploader,duration,live_status})j"
	maxSearch      = 20
)

// the codecs a screen names as it reports them, by yt-dlp's vcodec prefix
var codecFamilies = []struct{ prefix, family string }{
	{"avc1", "h264"}, {"avc3", "h264"}, {"h264", "h264"},
	{"vp09", "vp9"}, {"vp9", "vp9"},
	{"av01", "av1"},
	{"hev1", "hevc"}, {"hvc1", "hevc"}, {"hevc", "hevc"},
}

// CodecFamily names a yt-dlp vcodec ("avc1.640028") by its family ("h264"), "" for one a screen cannot name
func CodecFamily(vcodec string) string {
	v := strings.ToLower(vcodec)
	for _, c := range codecFamilies {
		if strings.HasPrefix(v, c.prefix) {
			return c.family
		}
	}
	return ""
}

type info struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Channel     string     `json:"channel"`
	Uploader    string     `json:"uploader"`
	Duration    float64    `json:"duration"`
	LiveStatus  string     `json:"live_status"`
	Thumbnail   string     `json:"thumbnail"`
	Description string     `json:"description"`
	Categories  []string   `json:"categories"`
	Track       string     `json:"track"`
	Artist      string     `json:"artist"`
	Album       string     `json:"album"`
	Formats     []ytFormat `json:"formats"`
}

type ytFormat struct {
	ID          string  `json:"format_id"`
	URL         string  `json:"url"`
	ManifestURL string  `json:"manifest_url"`
	Protocol    string  `json:"protocol"`
	VCodec      string  `json:"vcodec"`
	ACodec      string  `json:"acodec"`
	Ext         string  `json:"ext"`
	Height      int     `json:"height"`
	FPS         float64 `json:"fps"`
	TBR         float64 `json:"tbr"`
	ABR         float64 `json:"abr"`
	LangPref    int     `json:"language_preference"`
	Language    string  `json:"language"`
	FormatNote  string  `json:"format_note"`
	// SDR, HDR10, HDR12, HLG, DV
	DynamicRange string `json:"dynamic_range"`
	// AvailableAt is when YouTube starts serving the format's url (unix seconds): a fetch before it answers 403
	AvailableAt float64 `json:"available_at"`
}

func (f ytFormat) video() bool { return f.VCodec != "" && f.VCodec != "none" }
func (f ytFormat) audio() bool { return f.ACodec != "" && f.ACodec != "none" }
func (f ytFormat) hls() bool   { return strings.HasPrefix(f.Protocol, "m3u8") }

// Screen is a video's rendition set for a screen. HLS says every rendition url is an HLS playlist (a live video's
// variants), which the relay rewrites; otherwise each is one file fetched by ranges.
type Screen struct {
	catalog.Stream
	HLS bool
	// Info is what the video is: third-party text, memory only (D11), never logged
	Info Info
	// Audio is one audio-only format per language, the original first; the renditions carry the original's (or
	// the best one's) url as their AudioURL. empty for a live or muxed video
	Audio []AudioTrack
	// HDR is the dynamic range of each rendition, by index ("" for SDR)
	HDR []string
}

// Info is a video's own description as yt-dlp read it. Music is a video YouTube files under Music, or one it knows
// as a track (its artist and track fields).
type Info struct {
	Title        string
	Channel      string
	Description  string
	ThumbnailURL string
	DurationMS   int64
	Music        bool
	Track        string
	Artist       string
	Album        string
}

// AudioTrack is one language of a video's audio: YouTube's original, a dubbing (human or automatic) or a
// description track. URL is signed: never logged, stored or shown.
type AudioTrack struct {
	// ID names it for a select: "a-" and its language ("a-ru"), "a-und" for one without
	ID       string
	Language string
	// Label is yt-dlp's format note's language part ("Russian"), when it names one
	Label    string
	Original bool
	Dubbed   bool
	ACodec   string
	Bitrate  int
	URL      string
}

// ResolveScreen answers a rendition set of the video for a screen with profile p: video-only formats, one per
// height up to its maxHeight in the codecs it decodes (its order is its preference), and one audio format beside
// them; a live video gets its HLS variants instead, which carry their own audio. Stream.URL and AudioURL are the
// first (highest) rendition's. Every url is signed: never logged, stored or shown to the model.
func (p *Player) ResolveScreen(ctx context.Context, videoID string, prof catalog.DeviceProfile) (result Screen, resolveErr error) {
	if !ytmusic.ValidVideoID(videoID) {
		return Screen{}, providers.ErrUnplayable
	}
	ctx, finish := p.beginProgress(ctx, "screen")
	defer func() { finish(resolveErr) }()
	args := []string{"--print", screenTemplate}
	out, err := p.do(ctx, "screen", args, "https://www.youtube.com/watch?v="+videoID, maxScreenOutput, true)
	if err != nil {
		return Screen{}, err
	}
	var in info
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &in); err != nil {
		p.cfg.Log.Warn("ytdlp_failed", "mode", "screen", "code", "bad_output")
		return Screen{}, errors.New(providers.CodePlayerFailed)
	}
	s, ok := pickScreen(in, prof)
	if !ok {
		p.cfg.Log.Warn("ytdlp_failed", "mode", "screen", "code", "no_rendition", "formats", len(in.Formats))
		return Screen{}, providers.ErrUnplayable
	}
	p.cfg.Log.Info("ytdlp_screen", "renditions", len(s.Renditions), "live", s.Live, "separate_audio", s.AudioURL != "")
	if f := progressReporter(ctx); f != nil && len(s.Renditions) > 0 {
		r := s.Renditions[0]
		f(ResolveEvent{Stage: playprogress.StagePick, RemainingMS: -1, Height: r.Height, VCodec: r.VCodec,
			ACodec: r.ACodec, AudioTracks: len(s.Audio)})
	}
	if err := p.untilAvailable(ctx, in.Formats, s); err != nil {
		return Screen{}, err
	}
	return s, nil
}

// maxAvailableWait bounds the wait for a format youtube serves only a few seconds after it resolved
const maxAvailableWait = 8 * time.Second

// untilAvailable waits until youtube serves every url the screen will fetch: a url used before its format's
// available_at answers 403 (yt-dlp sleeps for it itself when it downloads; the relay fetches at once)
func (p *Player) untilAvailable(ctx context.Context, fs []ytFormat, s Screen) error {
	used := map[string]bool{s.URL: true, s.AudioURL: true}
	for _, r := range s.Renditions {
		used[r.URL], used[r.AudioURL] = true, true
	}
	for _, a := range s.Audio {
		used[a.URL] = true
	}
	var latest float64
	for _, f := range fs {
		if f.URL != "" && used[f.URL] && f.AvailableAt > latest {
			latest = f.AvailableAt
		}
	}
	return p.waitAvailable(ctx, latest)
}

// waitAvailable sleeps until availableAt (unix seconds, 0 for none), at most maxAvailableWait, reporting the countdown
func (p *Player) waitAvailable(ctx context.Context, availableAt float64) error {
	if availableAt <= 0 {
		return nil
	}
	wait := time.Unix(0, int64(availableAt*float64(time.Second))).Sub(p.now())
	if wait <= 0 {
		return nil
	}
	wait = min(wait, maxAvailableWait)
	p.cfg.Log.Info("ytdlp_available_wait", "ms", wait.Milliseconds())
	report := func(remaining time.Duration) {
		if f := progressReporter(ctx); f != nil {
			f(ResolveEvent{Stage: playprogress.StageWaitAvailable, RemainingMS: max(remaining.Milliseconds(), 0)})
		}
	}
	report(wait)
	t := time.NewTimer(wait)
	defer t.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	started := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			report(wait - time.Since(started))
		case <-t.C:
			report(0)
			return nil
		}
	}
}

func pickScreen(in info, prof catalog.DeviceProfile) (Screen, bool) {
	maxH := prof.MaxHeight
	if maxH <= 0 {
		maxH = defaultMaxHeight
	}
	codecs := prof.Codecs
	if len(codecs) == 0 {
		codecs = []string{"h264"}
	}
	rank := func(f ytFormat) int { return slices.Index(codecs, CodecFamily(f.VCodec)) }
	fits := func(f ytFormat) bool {
		return f.video() && f.Height > 0 && f.Height <= maxH && rank(f) >= 0 && usable(f.URL)
	}
	s := Screen{Stream: catalog.Stream{Live: in.LiveStatus == "is_live"}, Info: infoOf(in)}
	if in.Duration > 0 && !s.Live {
		s.DurationMS = int64(in.Duration * 1000)
	}
	var video, muxed, hls []ytFormat
	for _, f := range in.Formats {
		switch {
		case f.hls():
			if fits(f) {
				hls = append(hls, f)
			}
		case f.Protocol != "https" || strings.HasSuffix(f.ID, "-drc"):
		case fits(f) && !f.audio():
			video = append(video, f)
		case fits(f) && f.audio():
			muxed = append(muxed, f)
		}
	}
	better := func(a, b ytFormat) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(b.FPS, a.FPS), cmp.Compare(b.TBR, a.TBR))
	}
	var r []catalog.Rendition
	switch {
	case s.Live || len(video) == 0 && len(muxed) == 0:
		// a live video is HLS alone; a video with no adaptive format may still offer it
		if len(hls) == 0 {
			return s, false
		}
		r = perHeight(hls, better, "")
		s.HLS = true
	case len(video) > 0:
		a, ok := bestAudio(in.Formats)
		if !ok {
			r = perHeight(muxed, better, "")
			break
		}
		r = perHeight(video, better, a.URL)
		for i := range r {
			r[i].ACodec = a.ACodec
		}
		s.Expires = expiry(a.URL)
		s.Audio = audioTracks(in.Formats)
	default:
		r = perHeight(muxed, better, "")
	}
	if len(r) == 0 {
		return s, false
	}
	s.Renditions = r
	s.HDR = hdrOf(r, in.Formats)
	s.URL, s.AudioURL = r[0].URL, r[0].AudioURL
	for _, x := range r {
		if e := expiry(x.URL); !e.IsZero() && (s.Expires.IsZero() || e.Before(s.Expires)) {
			s.Expires = e
		}
	}
	return s, true
}

// perHeight keeps the best format of each height, highest first
func perHeight(fs []ytFormat, better func(a, b ytFormat) int, audioURL string) []catalog.Rendition {
	slices.SortStableFunc(fs, func(a, b ytFormat) int {
		return cmp.Or(cmp.Compare(b.Height, a.Height), better(a, b))
	})
	var out []catalog.Rendition
	for _, f := range fs {
		if len(out) > 0 && out[len(out)-1].Height == f.Height {
			continue
		}
		acodec := ""
		if f.audio() {
			acodec = f.ACodec
		}
		out = append(out, catalog.Rendition{
			Height: f.Height, FPS: int(f.FPS + 0.5), VCodec: CodecFamily(f.VCodec), ACodec: acodec,
			URL: f.URL, AudioURL: audioURL, Bitrate: int(f.TBR * 1000),
		})
		if len(out) == maxRenditions {
			break
		}
	}
	return out
}

// bestAudio is the audio-only format a screen plays beside its video: the original language first, then the
// highest bitrate, aac before opus at a tie
func bestAudio(fs []ytFormat) (ytFormat, bool) {
	var best ytFormat
	found := false
	for _, f := range fs {
		if f.video() || !f.audio() || f.Protocol != "https" || strings.HasSuffix(f.ID, "-drc") || !usable(f.URL) {
			continue
		}
		if !found || cmp.Or(cmp.Compare(f.LangPref, best.LangPref), cmp.Compare(f.ABR, best.ABR),
			cmp.Compare(boolInt(f.Ext == "m4a"), boolInt(best.Ext == "m4a"))) > 0 {
			best, found = f, true
		}
	}
	return best, found
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func usable(u string) bool { return strings.HasPrefix(u, "https://") && len(u) <= maxUpstreamURL }

// expiry reads a googlevideo url's expire, in its query or its path (/expire/<unix>/), zero without one
func expiry(raw string) time.Time {
	u, err := url.Parse(raw)
	if err != nil {
		return time.Time{}
	}
	v := u.Query().Get("expire")
	if v == "" {
		parts := strings.Split(u.Path, "/")
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] == "expire" {
				v = parts[i+1]
				break
			}
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// Video is one YouTube video a search found. Its text is third-party data: untrusted to the model, never logged.
type Video struct {
	ID        string
	Title     string
	Channel   string
	DurationS int
	Live      bool
}

// ThumbnailURL is the video's still on YouTube's image host
func (v Video) ThumbnailURL() string { return "https://i.ytimg.com/vi/" + v.ID + "/hqdefault.jpg" }

// SearchVideos asks YouTube for up to limit videos (at most 20) through yt-dlp's search, without cookies. The query
// reaches yt-dlp after --, never a log.
func (p *Player) SearchVideos(ctx context.Context, query string, limit int) ([]Video, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" {
		return nil, nil
	}
	limit = max(1, min(limit, maxSearch))
	args := []string{"--flat-playlist", "--yes-playlist", "--print", searchTemplate}
	out, err := p.do(ctx, "search", args, "ytsearch"+strconv.Itoa(limit)+":"+query, maxOutput*4, false)
	if err != nil {
		return nil, err
	}
	var vs []Video
	for line := range strings.SplitSeq(out, "\n") {
		var e struct {
			ID         string  `json:"id"`
			Title      string  `json:"title"`
			Channel    string  `json:"channel"`
			Uploader   string  `json:"uploader"`
			Duration   float64 `json:"duration"`
			LiveStatus string  `json:"live_status"`
		}
		if strings.TrimSpace(line) == "" || json.Unmarshal([]byte(line), &e) != nil || !ytmusic.ValidVideoID(e.ID) {
			continue
		}
		vs = append(vs, Video{
			ID: e.ID, Title: e.Title, Channel: cmp.Or(e.Channel, e.Uploader), DurationS: int(e.Duration + 0.5),
			Live: e.LiveStatus == "is_live",
		})
		if len(vs) == limit {
			break
		}
	}
	return vs, nil
}
