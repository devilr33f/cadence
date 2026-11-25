package media

import (
	"cmp"
	"math"
	"slices"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// longer strings are dropped field by field, as the device's own bounds would
// have cut them (R4)
const (
	maxIDBytes   = 63
	maxTextBytes = 255
	maxURLBytes  = 2047
)

const (
	stateIdle   = "idle"
	unspecified = "unspecified"
)

var knownActions = []string{"pause", "resume", "stop", "seek", "next", "previous"}

// MediaMetadata is what a session plays; a field past the device's bound is "".
type MediaMetadata struct {
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	ArtworkURL  string `json:"artworkUrl"`
	ArtworkHash string `json:"artworkHash" doc:"The key of the session's cover, empty without one: the hex SHA-256 of artworkUrl, or of the image the device uploaded when its source had no url (airplay); get-device-artwork serves the image."`
	Kind        string `json:"kind,omitempty" enum:"track,episode,movie,video,live,channel,podcast" doc:"What the item is; absent when the device does not say."`
	Series      string `json:"series,omitempty" doc:"The show an episode belongs to."`
	Season      uint32 `json:"season,omitempty" doc:"The episode's season; absent for none."`
	Episode     string `json:"episode,omitempty" doc:"The episode's label as its provider keys it (13, 5.5, OVA 1)."`
	EpisodeName string `json:"episodeName,omitempty" doc:"The episode's own name, when its provider has one beside the label."`
	Provider    string `json:"providerName,omitempty" doc:"The provider's display name the device reports (YouTube, TorrServer, AirPlay)."`
	// ProviderSource is the source under the provider the item played from (anime365 under torrplayer); absent for none
	ProviderSource string `json:"providerSource,omitempty"`
}

// MediaSession is a device's media session as admin callers see it (§5.8);
// the enums are stateName's, sourceName's and knownActions (a test holds them
// to the handler's answers).
type MediaSession struct {
	SessionID      string        `json:"sessionId"`
	Source         string        `json:"source" enum:"url,soloist,airplay,stream,video,screencast,external,group,bluetooth,unspecified"`
	State          string        `json:"state" enum:"idle,playing,paused,buffering,unspecified"`
	PositionMs     uint64        `json:"positionMs"`
	PositionAt     string        `json:"positionAt,omitempty" format:"date-time" doc:"When the device stamped positionMs; absent when it never did."`
	Rate           float32       `json:"rate"`
	DurationMs     uint64        `json:"durationMs"`
	BufferedMs     uint64        `json:"bufferedMs,omitempty" doc:"How far ahead of positionMs the player has buffered; absent when it does not say (only the screen's video player does)."`
	Buffering      *uint32       `json:"bufferingPercent,omitempty" minimum:"0" maximum:"100" doc:"How full the cache is towards resuming while the player waits for it (state buffering); absent otherwise."`
	Volume         *uint32       `json:"volume,omitempty"`
	Metadata       MediaMetadata `json:"metadata"`
	Actions        []string      `json:"actions" nullable:"false" enum:"pause,resume,stop,seek,next,previous"`
	Provider       string        `json:"provider,omitempty" doc:"Where a song the server's music queue plays was found (YouTube Music, Spotify); absent for other sessions."`
	ProviderSongID string        `json:"providerSongId,omitempty" doc:"The song's public id at its provider, without any access key; absent for other sessions."`
	WebURL         string        `json:"webUrl,omitempty" doc:"The song's public page, without query secrets or signed stream URLs; absent for other sessions."`
	// internal provider routing, not admin data
	ProviderInstance string `json:"-"`
	// the device that uploaded the cover artworkHash names, when it is another's: a group member shows its stream
	// source's (multiroom); "" for the device's own
	ArtworkDevice string `json:"-"`
	// internal play progress, not admin data: the first picture and a player failure token
	PictureReady bool   `json:"-"`
	ErrorCode    string `json:"-"`
}

// MediaIdle is what a device without a session shows.
type MediaIdle struct {
	State string `json:"state" enum:"idle"`
}

var none = MediaIdle{State: stateIdle}

func view(m *cadencev2.MediaSessionState) MediaSession {
	md := m.GetMetadata()
	v := MediaSession{
		SessionID:  bounded(m.GetSessionId(), maxIDBytes),
		Source:     sourceName(m.GetSource()),
		State:      stateName(m.GetState()),
		PositionMs: m.GetPositionMs(),
		DurationMs: m.GetDurationMs(),
		Metadata: MediaMetadata{
			Title: bounded(md.GetTitle(), maxTextBytes),
			// a video names its channel where a song names its artist: the line under the title either way
			Artist:         bounded(cmp.Or(md.GetArtist(), md.GetChannel().GetName()), maxTextBytes),
			Album:          bounded(md.GetAlbum(), maxTextBytes),
			ArtworkURL:     bounded(md.GetArtworkUrl(), maxURLBytes),
			Kind:           kindName(md.GetKind()),
			Series:         bounded(md.GetSeries(), maxTextBytes),
			Season:         md.GetSeason(),
			Episode:        bounded(md.GetEpisode(), maxIDBytes),
			EpisodeName:    bounded(md.GetEpisodeName(), maxTextBytes),
			Provider:       bounded(md.GetProvider().GetName(), maxTextBytes),
			ProviderSource: bounded(md.GetProvider().GetSource(), 64),
		},
		Actions:      []string{},
		PictureReady: m.GetPictureReady(),
		ErrorCode:    bounded(m.GetErrorCode(), 64),
	}
	v.Metadata.ArtworkHash = artworkHash(v.Metadata.ArtworkURL)
	if up := md.GetArtworkSha256(); v.Metadata.ArtworkURL == "" && sha256Hex.MatchString(up) {
		v.Metadata.ArtworkHash = up
	}
	// json has no NaN or infinity
	if r := float64(m.GetRate()); !math.IsNaN(r) && !math.IsInf(r, 0) {
		v.Rate = m.GetRate()
	}
	// 0: the device never stamped the position
	if at := m.GetPositionAtMs(); at != 0 {
		v.PositionAt = time.UnixMilli(at).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	}
	if m.Volume != nil {
		vol := m.GetVolume()
		v.Volume = &vol
	}
	v.BufferedMs = m.GetBufferedMs()
	if m.BufferingPercent != nil {
		p := min(m.GetBufferingPercent(), 100)
		v.Buffering = &p
	}
	for _, a := range m.GetActions() {
		if slices.Contains(knownActions, a) && !slices.Contains(v.Actions, a) {
			v.Actions = append(v.Actions, a)
		}
	}
	return v
}

func bounded(s string, limit int) string {
	if len(s) > limit {
		return ""
	}
	return s
}

func sourceName(s cadencev2.MediaSource) string {
	switch s {
	case cadencev2.MediaSource_MEDIA_SOURCE_URL:
		return "url"
	case cadencev2.MediaSource_MEDIA_SOURCE_SOLOIST:
		return "soloist"
	case cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY:
		return "airplay"
	case cadencev2.MediaSource_MEDIA_SOURCE_STREAM:
		return "stream"
	case cadencev2.MediaSource_MEDIA_SOURCE_VIDEO:
		return "video"
	case cadencev2.MediaSource_MEDIA_SOURCE_SCREENCAST:
		return "screencast"
	case cadencev2.MediaSource_MEDIA_SOURCE_EXTERNAL:
		return "external"
	case cadencev2.MediaSource_MEDIA_SOURCE_GROUP:
		return "group"
	case cadencev2.MediaSource_MEDIA_SOURCE_BLUETOOTH:
		return "bluetooth"
	}
	return unspecified
}

func kindName(k cadencev2.MediaKind) string {
	switch k {
	case cadencev2.MediaKind_MEDIA_KIND_TRACK:
		return "track"
	case cadencev2.MediaKind_MEDIA_KIND_EPISODE:
		return "episode"
	case cadencev2.MediaKind_MEDIA_KIND_MOVIE:
		return "movie"
	case cadencev2.MediaKind_MEDIA_KIND_VIDEO:
		return "video"
	case cadencev2.MediaKind_MEDIA_KIND_LIVE:
		return "live"
	case cadencev2.MediaKind_MEDIA_KIND_CHANNEL:
		return "channel"
	case cadencev2.MediaKind_MEDIA_KIND_PODCAST:
		return "podcast"
	}
	return ""
}

func stateName(s cadencev2.PlaybackState) string {
	switch s {
	case cadencev2.PlaybackState_PLAYBACK_STATE_IDLE:
		return stateIdle
	case cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING:
		return "playing"
	case cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED:
		return "paused"
	case cadencev2.PlaybackState_PLAYBACK_STATE_BUFFERING:
		return "buffering"
	}
	return unspecified
}
