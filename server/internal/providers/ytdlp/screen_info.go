package ytdlp

import (
	"cmp"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

const (
	// a description is cut to this many code points: the screen and the dashboard show its start
	maxDescription = 500
	maxText        = 256
	// at most this many audio languages (YouTube dubs a popular video into ~20)
	maxAudioTracks = 24
)

// infoOf is the video's own text, bounded; the thumbnail is YouTube's image host's alone
func infoOf(in info) Info {
	i := Info{
		Title: cut(in.Title, maxText), Channel: cut(cmp.Or(in.Channel, in.Uploader), maxText),
		Description: cut(strings.TrimSpace(in.Description), maxDescription),
		Track:       cut(in.Track, maxText), Artist: cut(in.Artist, maxText), Album: cut(in.Album, maxText),
		ThumbnailURL: thumbnail(in.ID, in.Thumbnail),
	}
	if in.Duration > 0 && in.LiveStatus != "is_live" {
		i.DurationMS = int64(in.Duration * 1000)
	}
	i.Music = slices.Contains(in.Categories, "Music") || in.Track != "" && in.Artist != ""
	return i
}

// thumbnail is yt-dlp's still when YouTube's image host serves it, else the video's hqdefault there
func thumbnail(id, raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Scheme == "https" && u.User == nil &&
		(u.Host == "i.ytimg.com" || u.Host == "i1.ytimg.com" || u.Host == "i9.ytimg.com") && len(raw) <= 1024 {
		return raw
	}
	if id == "" {
		return ""
	}
	return Video{ID: id}.ThumbnailURL()
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

var langTag = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// audioTracks is the best audio-only format of each language, the original first, then by language; a video with
// one language answers its one track
func audioTracks(fs []ytFormat) []AudioTrack {
	best := map[string]ytFormat{}
	var order []string
	for _, f := range fs {
		if f.video() || !f.audio() || f.Protocol != "https" || strings.HasSuffix(f.ID, "-drc") || !usable(f.URL) {
			continue
		}
		lang := f.Language
		if !langTag.MatchString(lang) {
			lang = "und"
		}
		b, ok := best[lang]
		if !ok {
			order = append(order, lang)
		}
		if !ok || cmp.Or(cmp.Compare(f.LangPref, b.LangPref), cmp.Compare(f.ABR, b.ABR),
			cmp.Compare(boolInt(f.Ext == "m4a"), boolInt(b.Ext == "m4a"))) > 0 {
			best[lang] = f
		}
	}
	out := make([]AudioTrack, 0, len(order))
	for _, lang := range order {
		f := best[lang]
		note := strings.ToLower(f.FormatNote)
		out = append(out, AudioTrack{
			ID: "a-" + lang, Language: lang, Label: noteLabel(f.FormatNote),
			Original: f.LangPref >= 10 || strings.Contains(note, "original"),
			Dubbed:   strings.Contains(note, "dubbed"),
			ACodec:   f.ACodec, Bitrate: int(f.ABR * 1000), URL: f.URL,
		})
	}
	slices.SortStableFunc(out, func(a, b AudioTrack) int {
		return cmp.Or(cmp.Compare(boolInt(b.Original), boolInt(a.Original)), cmp.Compare(a.Language, b.Language))
	})
	if len(out) > maxAudioTracks {
		out = out[:maxAudioTracks]
	}
	return out
}

// noteLabel is a format note's language name: "Russian (dubbed-auto), medium" is "Russian"; "" when the note names
// only a quality
func noteLabel(note string) string {
	head, _, _ := strings.Cut(note, ",")
	head, _, _ = strings.Cut(head, "(")
	head = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(head), "original"))
	switch strings.ToLower(head) {
	case "", "low", "medium", "high", "ultralow", "default":
		return ""
	}
	return cut(head, 64)
}

// hdrOf names each rendition's dynamic range by the format its url came from, "" for SDR
func hdrOf(rs []catalog.Rendition, fs []ytFormat) []string {
	out := make([]string, len(rs))
	found := false
	for i, r := range rs {
		for _, f := range fs {
			if f.URL == r.URL && f.DynamicRange != "" && f.DynamicRange != "SDR" {
				out[i], found = cut(f.DynamicRange, 16), true
			}
		}
	}
	if !found {
		return nil
	}
	return out
}
