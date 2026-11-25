package torrplayer

import "time"

// the wire shapes of torrplayer's control API (/api/v1), the fields this
// client reads; every text in them is third-party (titles, descriptions,
// translators) and never reaches a log

type statusOut struct {
	Version   string   `json:"version"`
	Providers []string `json:"providers"`
	Features  []string `json:"features"`
}

type titleDTO struct {
	Provider      string   `json:"provider"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	NameRu        string   `json:"name_ru,omitempty"`
	NameEn        string   `json:"name_en,omitempty"`
	Year          int      `json:"year,omitempty"`
	Kind          string   `json:"kind,omitempty"`
	EpisodesTotal int      `json:"episodes_total,omitempty"`
	EpisodesAired int      `json:"episodes_aired,omitempty"`
	Ongoing       bool     `json:"ongoing,omitempty"`
	Poster        string   `json:"poster,omitempty"`
	ShikimoriID   int      `json:"shikimori_id,omitempty"`
	MALID         int      `json:"mal_id,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	Description   string   `json:"description,omitempty"`
	Studios       []string `json:"studios,omitempty"`
	Season        string   `json:"season,omitempty"`
	Status        string   `json:"status,omitempty"`
	Score         float64  `json:"score,omitempty"`
	AgeRating     string   `json:"age_rating,omitempty"`
	Available     bool     `json:"available"`
}

type searchOut struct {
	Titles     []titleDTO `json:"titles"`
	Count      int        `json:"count"`
	Errors     []string   `json:"errors,omitempty"`
	Page       int        `json:"page,omitempty"`
	HasMore    bool       `json:"has_more,omitempty"`
	Unenforced []string   `json:"unenforced,omitempty"`
	Source     string     `json:"source,omitempty"`
}

type episodeDTO struct {
	Ordinal     float64 `json:"ordinal"`
	Label       string  `json:"label"`
	ID          string  `json:"id,omitempty"`
	Name        string  `json:"name,omitempty"`
	Duration    int     `json:"duration,omitempty"`
	Type        string  `json:"type,omitempty"`
	Watched     bool    `json:"watched,omitempty"`
	ResumeSec   float64 `json:"resume_sec,omitempty"`
	DurationSec float64 `json:"duration_sec,omitempty"`
}

type episodesOut struct {
	Provider    string       `json:"provider"`
	TitleID     string       `json:"title_id"`
	Episodes    []episodeDTO `json:"episodes"`
	Count       int          `json:"count"`
	NextEpisode string       `json:"next_episode,omitempty"`
}

// source kinds as torrplayer names them
const (
	kindTorrent = "torrent"
	kindHLS     = "hls"
	kindStream  = "stream"
)

type sourceDTO struct {
	Provider   string `json:"provider"`
	TitleID    string `json:"title_id"`
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Label      string `json:"label,omitempty"`
	Quality    int    `json:"quality,omitempty"`
	Codec      string `json:"codec,omitempty"`
	Translator string `json:"translator,omitempty"`
	TransType  string `json:"trans_type,omitempty"`
	Lang       string `json:"lang,omitempty"`
	Episodes   string `json:"episodes,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	Seeders    int    `json:"seeders,omitempty"`
	Leechers   int    `json:"leechers,omitempty"`
	InfoHash   string `json:"info_hash,omitempty"`
	Hardsub    bool   `json:"hardsub,omitempty"`
	Hidden     bool   `json:"hidden,omitempty"`
}

type sourcesOut struct {
	Provider    string      `json:"provider"`
	TitleID     string      `json:"title_id"`
	Episode     string      `json:"episode,omitempty"`
	Sources     []sourceDTO `json:"sources"`
	Count       int         `json:"count"`
	HiddenCount int         `json:"hidden_count"`
}

type resolveIn struct {
	Provider string `json:"provider"`
	TitleID  string `json:"title_id,omitempty"`
	SourceID string `json:"source_id"`
	Episode  string `json:"episode,omitempty"`
	Quality  int    `json:"quality,omitempty"`
}

type subtitleDTO struct {
	URL    string `json:"url"`
	Format string `json:"format,omitempty"`
	Lang   string `json:"lang,omitempty"`
	Label  string `json:"label,omitempty"`
}

type skipDTO struct {
	Kind  string  `json:"kind"`
	Start float64 `json:"start"`
	Stop  float64 `json:"stop"`
}

type mediaOut struct {
	Kind      string            `json:"kind"`
	URL       string            `json:"url"`
	Episode   string            `json:"episode,omitempty"`
	Quality   int               `json:"quality,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Subtitles []subtitleDTO     `json:"subtitles,omitempty"`
	Skips     []skipDTO         `json:"skips,omitempty"`
	InfoHash  string            `json:"info_hash,omitempty"`
	Index     int               `json:"index,omitempty"`
}

type skipsOut struct {
	Provider string    `json:"provider"`
	TitleID  string    `json:"title_id"`
	Episode  string    `json:"episode,omitempty"`
	Skips    []skipDTO `json:"skips"`
	Count    int       `json:"count"`
}

type episodeProgressDTO struct {
	Episode     string    `json:"episode"`
	Watched     bool      `json:"watched"`
	PositionSec float64   `json:"position_sec,omitempty"`
	DurationSec float64   `json:"duration_sec,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
}

type prefsDTO struct {
	Translator string `json:"translator,omitempty"`
	Group      string `json:"group,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Quality    int    `json:"quality,omitempty"`
	Provider   string `json:"provider,omitempty"`
}

type progressOut struct {
	Provider      string               `json:"provider"`
	TitleID       string               `json:"title_id"`
	Found         bool                 `json:"found"`
	Name          string               `json:"name,omitempty"`
	ShikimoriID   int                  `json:"shikimori_id,omitempty"`
	EpisodesTotal int                  `json:"episodes_total,omitempty"`
	LastEpisode   string               `json:"last_episode,omitempty"`
	Episodes      []episodeProgressDTO `json:"episodes,omitempty"`
	Prefs         prefsDTO             `json:"prefs,omitzero"`
	Poster        string               `json:"poster,omitempty"`
}

type titlePrefsIn struct {
	Translator string `json:"translator,omitempty"`
	Group      string `json:"group,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Quality    int    `json:"quality,omitempty"`
	Clear      bool   `json:"clear,omitempty"`
}

type progressReportIn struct {
	Episode     string  `json:"episode"`
	PositionSec float64 `json:"position"`
	DurationSec float64 `json:"duration,omitempty"`
	State       string  `json:"state,omitempty"`
}

type watchedIn struct {
	Episode string `json:"episode"`
	Watched bool   `json:"watched"`
}

type watchingItemDTO struct {
	Provider      string    `json:"provider"`
	TitleID       string    `json:"title_id"`
	Name          string    `json:"name,omitempty"`
	Poster        string    `json:"poster,omitempty"`
	ShikimoriID   int       `json:"shikimori_id,omitempty"`
	EpisodesTotal int       `json:"episodes_total,omitempty"`
	WatchedCount  int       `json:"watched_count"`
	LastEpisode   string    `json:"last_episode,omitempty"`
	LastWatchedAt time.Time `json:"last_watched_at,omitzero"`
	ResumeEpisode string    `json:"resume_episode,omitempty"`
	ResumeSec     int       `json:"resume_sec,omitempty"`
}

type watchingOut struct {
	Items []watchingItemDTO `json:"items"`
	Count int               `json:"count"`
}

type myListItemDTO struct {
	ShikimoriID   int    `json:"shikimori_id"`
	MALID         int    `json:"mal_id,omitempty"`
	Name          string `json:"name"`
	Poster        string `json:"poster,omitempty"`
	Status        string `json:"status"`
	Score         int    `json:"score,omitempty"`
	Watched       int    `json:"watched"`
	EpisodesTotal int    `json:"episodes_total,omitempty"`
	EpisodesAired int    `json:"episodes_aired,omitempty"`
	Kind          string `json:"kind,omitempty"`
}

type myListOut struct {
	Status string          `json:"status"`
	Items  []myListItemDTO `json:"items"`
	Count  int             `json:"count"`
}

type myListResolveOut struct {
	ShikimoriID int    `json:"shikimori_id"`
	Provider    string `json:"provider"`
	TitleID     string `json:"title_id"`
	Episode     string `json:"episode,omitempty"`
}

type myListSetIn struct {
	Status string `json:"status,omitempty"`
}

type subscriptionDTO struct {
	Provider    string `json:"provider"`
	TitleID     string `json:"title_id"`
	Name        string `json:"name"`
	ShikimoriID int    `json:"shikimori_id,omitempty"`
	Paused      bool   `json:"paused"`
}

type subscriptionsOut struct {
	Subscriptions []subscriptionDTO `json:"subscriptions"`
	Count         int               `json:"count"`
}

type subscribeIn struct {
	Provider string `json:"provider"`
	TitleID  string `json:"title_id"`
}

type errorBody struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}
