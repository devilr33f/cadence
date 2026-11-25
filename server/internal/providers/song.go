package providers

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// SongIdentity is a song's durable, public identity. It contains no provider
// access key, stream url, signed url or relay url.
type SongIdentity struct {
	Provider string
	SongID   string
	WebURL   string
}

var (
	youtubeSongID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	spotifySongID = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
	numericSongID = regexp.MustCompile(`^[0-9]{1,20}$`)
	vkSongID      = regexp.MustCompile(`^-?[0-9]{1,20}_[0-9]{1,20}$`)
	yandexSongURL = regexp.MustCompile(`^/(?:album/[0-9]{1,20}/)?track/[0-9]{1,20}/?$`)
)

// MusicProvider is a track source as play history and durable song state name
// it. ytmusic is the history's existing name for SourceYouTube.
func MusicProvider(source string) string {
	switch source {
	case SourceYouTube:
		return "ytmusic"
	case SourceSpotify, SourceYandex, SourceZvuk, SourceSoundCloud, SourceVK:
		return source
	}
	return ""
}

// MusicSource is the inverse of MusicProvider.
func MusicSource(provider string) string {
	if provider == "ytmusic" {
		return SourceYouTube
	}
	switch provider {
	case "spotify", "yandex", "zvuk", "soundcloud", "vk":
		return provider
	}
	return ""
}

// RadioStation is the station a radio queue plays, including the derived
// station of a Yandex radio's initial seed.
func RadioStation(t Track) string {
	if t.Station != "" {
		return t.Station
	}
	if t.Source == SourceYandex && numericSongID.MatchString(t.ID) {
		return "track:" + t.ID
	}
	return ""
}

// PublicSongIdentity strips a VK access key and canonicalizes the public page.
// A SoundCloud page keeps its permalink but loses query and fragment, which can
// carry a secret token. A missing public page does not prevent a local favorite.
func PublicSongIdentity(t Track) (SongIdentity, bool) {
	out := SongIdentity{Provider: MusicProvider(t.Source), SongID: t.ID}
	if out.Provider == "" {
		return SongIdentity{}, false
	}
	switch t.Source {
	case SourceYouTube:
		if !youtubeSongID.MatchString(t.ID) {
			return SongIdentity{}, false
		}
	case SourceSpotify:
		if !spotifySongID.MatchString(t.ID) {
			return SongIdentity{}, false
		}
	case SourceYandex, SourceZvuk, SourceSoundCloud:
		if !numericSongID.MatchString(t.ID) {
			return SongIdentity{}, false
		}
	case SourceVK:
		parts := strings.SplitN(t.ID, "_", 3)
		if len(parts) < 2 {
			return SongIdentity{}, false
		}
		out.SongID = parts[0] + "_" + parts[1]
		if !vkSongID.MatchString(out.SongID) {
			return SongIdentity{}, false
		}
	}
	out.WebURL = PublicSongWebURL(out.Provider, out.SongID, t.WebURL)
	return out, true
}

// PublicSongWebURL is the song's public page as history may store it: the
// provider's canonical form, or the raw page when it is the provider's own and
// carries no query or fragment. "" when none is safe to keep.
func PublicSongWebURL(provider, songID, raw string) string {
	var canonical string
	switch source := MusicSource(provider); source {
	case SourceYouTube:
		if !youtubeSongID.MatchString(songID) {
			return ""
		}
		canonical = "https://music.youtube.com/watch?v=" + songID
	case SourceSpotify:
		if !spotifySongID.MatchString(songID) {
			return ""
		}
		canonical = "https://open.spotify.com/track/" + songID
	case SourceYandex:
		if !numericSongID.MatchString(songID) {
			return ""
		}
		canonical = "https://music.yandex.ru/track/" + songID
		if u, ok := publicURL(raw, "music.yandex.ru"); ok && yandexSongURL.MatchString(u.Path) &&
			strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), "/track/"+songID) {
			return u.String()
		}
	case SourceZvuk:
		if !numericSongID.MatchString(songID) {
			return ""
		}
		canonical = "https://zvuk.com/track/" + songID
	case SourceSoundCloud:
		if !numericSongID.MatchString(songID) {
			return ""
		}
		if u, ok := publicURL(raw, "soundcloud.com"); ok && u.Path != "" && u.Path != "/" {
			return u.String()
		}
		return ""
	case SourceVK:
		if !vkSongID.MatchString(songID) {
			return ""
		}
		return "https://vk.com/audio" + songID
	default:
		return ""
	}
	return canonical
}

func publicURL(raw, host string) (*url.URL, bool) {
	if raw == "" || len(raw) > 2047 || !utf8.ValidString(raw) {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), host) || u.Port() != "" || u.User != nil {
		return nil, false
	}
	u.Host = host
	u.RawQuery, u.Fragment = "", ""
	return u, true
}
