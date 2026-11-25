package catalog

import "time"

// ProviderID names a Cadence provider.
type ProviderID string

const (
	ProviderTorrplayer   ProviderID = "torrplayer"
	ProviderTorrServer   ProviderID = "torrserver"
	ProviderYouTubeMusic ProviderID = "ytmusic"
	ProviderSpotify      ProviderID = "spotify"
)

// SourceID names a source inside a provider ("aniliberty", "nyaa",
// "torznab:jackett"); "" for a provider without sources.
type SourceID string

// Kind is what an item is.
type Kind string

const (
	KindTitleGroup Kind = "title_group"
	KindSeries     Kind = "series"
	KindSeason     Kind = "season"
	KindEpisode    Kind = "episode"
	KindMovie      Kind = "movie"
	KindVideo      Kind = "video"
	KindLive       Kind = "live"
	KindChannel    Kind = "channel"
	KindPlaylist   Kind = "playlist"
	KindTrack      Kind = "track"
	KindAlbum      Kind = "album"
	KindArtist     Kind = "artist"
	KindTorrent    Kind = "torrent"
	KindFile       Kind = "file"
	KindFolder     Kind = "folder"
)

// Kinds lists every item kind.
var Kinds = []Kind{
	KindTitleGroup, KindSeries, KindSeason, KindEpisode, KindMovie, KindVideo, KindLive, KindChannel, KindPlaylist,
	KindTrack, KindAlbum, KindArtist, KindTorrent, KindFile, KindFolder,
}

// Item is anything a provider lists. Key is the provider's own identity; Ref
// is empty from a provider and set by the service before the item leaves the
// server. Every text field is third-party data.
type Item struct {
	Key Key `json:"-"`
	// Ref is what devices and the model see and echo back
	Ref      Ref        `json:"ref,omitempty"`
	Provider ProviderID `json:"provider"`
	Source   SourceID   `json:"source,omitempty"`
	Kind     Kind       `json:"kind"`

	Title    string  `json:"title"`
	Subtitle string  `json:"subtitle,omitempty"`
	Artwork  Artwork `json:"artwork,omitzero"`
	// DurationS is 0 when unknown
	DurationS int       `json:"durationS,omitempty"`
	Badges    []Badge   `json:"badges,omitempty"`
	Progress  *Progress `json:"progress,omitempty"`
	// Playable says Resolve takes this item (with Options where it has them)
	Playable bool `json:"playable"`
	// HasChildren says Item lists children (episodes, tracks, files, variants)
	HasChildren bool `json:"hasChildren"`
	// Unavailable is a catalogue row no enabled source can play ("exists,
	// but nobody hosts it")
	Unavailable bool `json:"unavailable,omitempty"`

	// typed per kind; at most one is set
	Show    *ShowInfo    `json:"show,omitempty"`
	Episode *EpisodeInfo `json:"episode,omitempty"`
	Music   *MusicInfo   `json:"music,omitempty"`
	Torrent *TorrentInfo `json:"torrent,omitempty"`
	File    *FileInfo    `json:"file,omitempty"`

	// Stats are the public counts the provider showed (YouTube's views and
	// likes, a channel's subscribers), nil where it showed none
	Stats *Stats `json:"stats,omitempty"`
	// Owner is a video's or a playlist's channel: an item of its own, which
	// the caller opens by its Ref
	Owner *Owner `json:"owner,omitempty"`
}

// Stats are an item's public counts: numbers only. Zero is "not shown". A
// relative "3 weeks ago" sets PublishedApprox; DislikesEstimated is Return
// YouTube Dislike's estimate (the youtubeVotes setting), never YouTube's own.
type Stats struct {
	Views             int64     `json:"views,omitempty"`
	Likes             int64     `json:"likes,omitempty"`
	DislikesEstimated int64     `json:"dislikesEstimated,omitempty"`
	Plays             int64     `json:"plays,omitempty"`
	Subscribers       int64     `json:"subscribers,omitempty"`
	MonthlyAudience   int64     `json:"monthlyAudience,omitempty"`
	VideoCount        int       `json:"videoCount,omitempty"`
	PublishedAt       time.Time `json:"publishedAt,omitzero"`
	PublishedApprox   bool      `json:"publishedApprox,omitempty"`
}

// Owner is the channel an item belongs to. Key is the provider's (empty when
// it cannot be opened); the service issues Ref from it.
type Owner struct {
	Key   Key    `json:"-"`
	Ref   Ref    `json:"ref,omitempty"`
	Title string `json:"title"`
}

// Artwork is a provider's image, which the server's image proxy fetches and
// serves by hash; URL never leaves the server.
type Artwork struct {
	URL    string `json:"-"`
	Aspect string `json:"aspect,omitempty"`
	// Hash is the image proxy's, set by the service
	Hash string `json:"hash,omitempty"`
}

// BadgeKind follows cards.BadgeKind, plus the catalog's own marks.
type BadgeKind string

const (
	BadgeLive     BadgeKind = "live"
	BadgePremiere BadgeKind = "premiere"
	BadgeHD       BadgeKind = "hd"
	Badge4K       BadgeKind = "4k"
	BadgeHDR      BadgeKind = "hdr"
	BadgeCC       BadgeKind = "cc"
	BadgeExplicit BadgeKind = "explicit"
	BadgeNew      BadgeKind = "new"
	BadgeOngoing  BadgeKind = "ongoing"
	// voice, sub and raw mark a stream's voiceover kind
	BadgeVoice   BadgeKind = "voice"
	BadgeSub     BadgeKind = "sub"
	BadgeRaw     BadgeKind = "raw"
	BadgeTorrent BadgeKind = "torrent"
	BadgeHardsub BadgeKind = "hardsub"
)

type Badge struct {
	Kind  BadgeKind `json:"kind"`
	Label string    `json:"label,omitempty"`
}

// Progress is the owner's on this item.
type Progress struct {
	Watched   bool      `json:"watched,omitempty"`
	PositionS int       `json:"positionS,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
	// Count and Total are watched episodes of a series ("5 / 24")
	Count int `json:"count,omitempty"`
	Total int `json:"total,omitempty"`
}

// Names are a title's names: Russian, English and alternatives.
type Names struct {
	RU  string   `json:"ru,omitempty"`
	EN  string   `json:"en,omitempty"`
	Alt []string `json:"alt,omitempty"`
}

// ExternalIDs are the shared ids that merge one title across sources.
type ExternalIDs struct {
	Shikimori string `json:"shikimori,omitempty"`
	MAL       string `json:"mal,omitempty"`
}

// ShowInfo is a title group's, series', season's or movie's: torrplayer's
// search fields.
type ShowInfo struct {
	Names Names `json:"names,omitzero"`
	Year  int   `json:"year,omitempty"`
	// Format is the source's own: TV, movie, OVA, ONA, special
	Format          string      `json:"format,omitempty"`
	EpisodesAired   int         `json:"episodesAired,omitempty"`
	EpisodesTotal   int         `json:"episodesTotal,omitempty"`
	Status          string      `json:"status,omitempty"` // announced, ongoing, released
	Season          string      `json:"season,omitempty"` // winter, spring, summer, autumn
	Genres          []string    `json:"genres,omitempty"`
	Studios         []string    `json:"studios,omitempty"`
	Score           float64     `json:"score,omitempty"` // out of 10
	AgeRating       string      `json:"ageRating,omitempty"`
	Description     string      `json:"description,omitempty"`
	IDs             ExternalIDs `json:"ids,omitzero"`
	Voiceovers      []string    `json:"voiceovers,omitempty"`
	SourcesWithIt   int         `json:"sourcesWithIt,omitempty"` // a title group's variant count
	NextEpisodeAirs time.Time   `json:"nextEpisodeAirs,omitzero"`
	// from Shikimori and MyAnimeList (TitleInfo), on a title's own page
	Rank       int    `json:"rank,omitempty"`       // MyAnimeList's by score
	Popularity int    `json:"popularity,omitempty"` // MyAnimeList's by members
	AiredOn    string `json:"airedOn,omitempty"`    // yyyy-mm-dd
	ReleasedOn string `json:"releasedOn,omitempty"` // yyyy-mm-dd, finished titles
	// Related are the title's seasons, sequels and the rest of its franchise
	Related []RelatedTitle `json:"related,omitempty"`
	// Mine is the owner's own list entry (the list torrplayer keeps linked), nil when none
	Mine *UserRate `json:"mine,omitempty"`
}

// RelatedTitle is a title Shikimori relates to another: its relation in the owner's language («Продолжение»).
type RelatedTitle struct {
	Relation  string `json:"relation"`
	Title     string `json:"title"`
	Kind      string `json:"kind,omitempty"`
	Year      int    `json:"year,omitempty"`
	Shikimori string `json:"shikimori,omitempty"`
}

// UserRate is the owner's list entry of a title: its status (planned, watching, completed, on_hold, dropped,
// rewatching), score out of 10 (0: none) and episodes watched.
type UserRate struct {
	Status  string `json:"status"`
	Score   int    `json:"score,omitempty"`
	Watched int    `json:"watched,omitempty"`
}

// EpisodeInfo is one episode. Label is the key ("5.5" for a recap): it is
// what Options, Resolve and progress name; Ordinal only sorts.
type EpisodeInfo struct {
	Label   string  `json:"label"`
	Ordinal float64 `json:"ordinal"`
	Name    string  `json:"name,omitempty"`
	// Type is tv, ova, movie, special, recap…
	Type string `json:"type,omitempty"`
}

// MusicInfo is a track's, album's, artist's or playlist's.
type MusicInfo struct {
	Artist   string `json:"artist,omitempty"`
	Album    string `json:"album,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Tracks   int    `json:"tracks,omitempty"`
	Explicit bool   `json:"explicit,omitempty"`
}

// TorrentInfo is a TorrServer torrent's or a torrent source's.
type TorrentInfo struct {
	SizeBytes int64 `json:"sizeBytes,omitempty"`
	Seeders   int   `json:"seeders,omitempty"`
	Leechers  int   `json:"leechers,omitempty"`
	Files     int   `json:"files,omitempty"`
}

// FileInfo is a file inside a torrent or a folder.
type FileInfo struct {
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	Index     int    `json:"index"`
	Path      string `json:"path,omitempty"`
	Video     bool   `json:"video,omitempty"`
	Subtitle  bool   `json:"subtitle,omitempty"`
}
