// Package providers holds the module registry, the STT/TTS/LLM contracts the
// voice coordinator uses, and provider instances stored in the database.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Kind string

const (
	KindSTT     Kind = "stt"
	KindTTS     Kind = "tts"
	KindLLM     Kind = "llm"
	KindWeather Kind = "weather"
	KindSearch  Kind = "search"
	KindEmbed   Kind = "embed"
	// KindHermes is an agent server that runs background tasks; its module
	// lives with the integration that uses it.
	KindHermes Kind = "hermes"
	// KindHomeAssistant is the owner's Home Assistant; its module lives with
	// the ha integration.
	KindHomeAssistant Kind = "homeassistant"
	// KindMCP is one MCP server whose tools the agent may offer; its module
	// lives with the mcp integration.
	KindMCP Kind = "mcp"
	// KindOTLP is an OTLP/HTTP collector the server forwards stored logs and
	// metrics to; its module is otlphttp.
	KindOTLP Kind = "otlp"
	// KindMusic finds songs the agent may play: youtube-music, spotify,
	// yandex-music, zvuk, soundcloud or vk-music.
	KindMusic Kind = "music"
	// KindPlayer turns a found song into a stream the device plays: yt-dlp.
	KindPlayer Kind = "player"
	// KindXSearch searches posts on X: x-web.
	KindXSearch Kind = "xsearch"
	// KindT3Code is a T3 Code server whose threads Cadence follows and
	// steers; its module lives with the t3code integration.
	KindT3Code Kind = "t3code"
	// KindCatalog is a content provider of the catalog (torrplayer,
	// TorrServer); its module lives with its integration and New answers a
	// catalog.Provider.
	KindCatalog Kind = "catalog"
)

// Module describes one provider implementation. New returns the contract
// matching Kind (STT, TTS, LLM, Weather, Search, Embed, Music or Player; for KindHermes,
// KindHomeAssistant, KindMCP, KindOTLP and KindT3Code their package's client). Every key in
// Secrets must be set; OptionalSecrets may be left out.
type Module struct {
	ID              string
	Kind            Kind
	ConfigSchema    string
	Secrets         []string
	OptionalSecrets []string
	// Check, when set, refuses a config ConfigSchema took, by the JSON pointer
	// of what fails and never its value: a rule a schema cannot say
	Check func(cfg json.RawMessage) (path string, ok bool)
	New   func(cfg json.RawMessage, secrets map[string]string) (any, error)
	// NewSaving, when set, is used instead of New and gets save: a module
	// whose secret the upstream renews (cookies a site rotates) writes the
	// renewed value back with it, sealed as at create. A probe's build gets a
	// save that keeps nothing.
	NewSaving func(cfg json.RawMessage, secrets map[string]string, save SaveSecret) (any, error)
	// Probe, when set, is the module's provider test (M7bs-D2): its cheapest
	// call that costs nothing, on an instance New built for the probe alone,
	// which it may connect and close. It answers nil or a ProbeError, never
	// upstream text; without one a test answers unsupported.
	Probe func(ctx context.Context, instance any) error
}

// SaveSecret stores a new value of one of an instance's own secrets. It fails
// with ErrNotFound once the instance it was built from is gone (deleted, even
// if one was created again under its name), and never quotes the value.
type SaveSecret func(ctx context.Context, key, value string) error

// failure codes carried by STTEvent.Code, LLMChunk.Err and TTS errors; never upstream text
const (
	CodeSTTUnavailable = "stt_unavailable"
	CodeSTTFailed      = "stt_failed"
	CodeLLMFailed      = "llm_failed"
	CodeTTSFailed      = "tts_failed"
	CodeWeatherFailed  = "weather_failed"
	CodeSearchFailed   = "search_failed"
	CodeMusicFailed    = "music_failed"
	CodePlayerFailed   = "player_failed"
	CodeXSearchFailed  = "x_search_failed"

	CodeEmbedUnavailable = "embed_unavailable"
	CodeEmbedFailed      = "embed_failed"
	CodeEmbedTimeout     = "embed_timeout"
)

type STT interface {
	Start(ctx context.Context, o STTOptions) (STTStream, error)
}

type STTOptions struct {
	SampleRate int
	Language   string
}

type STTStream interface {
	Write(opus []byte) error
	Events() <-chan STTEvent
	Close() error
}

type STTEventKind int

const (
	STTSpeechStarted STTEventKind = iota + 1
	STTFinal
	STTFailed
	// STTPartial is an interim hypothesis of the utterance so far, whole: a
	// later one replaces it, the final ends it. A stream may send none.
	STTPartial
)

type STTEvent struct {
	Kind STTEventKind
	Text string
	Code string
	// Detail is an STTFailed's cause as a codes-only token (closed_1011,
	// asr_error_3), for the session's spans and logs; "" when unknown
	Detail string
}

type LLM interface {
	Stream(ctx context.Context, r LLMRequest) (<-chan LLMChunk, error)
}

// Message roles: system, user, assistant, tool. A tool message answers ToolCallID.
type Message struct {
	Role, Content, ToolCallID string
	ToolCalls                 []ToolCall
}

type ToolSpec struct {
	Name, Description string
	Parameters        json.RawMessage
}

// ToolCall arguments are the model's JSON; text that is not valid JSON arrives
// as a JSON string so the caller's validator rejects it.
type ToolCall struct {
	ID, Name  string
	Arguments json.RawMessage
}

type Usage struct{ PromptTokens, CompletionTokens int }

// ToolChoice is auto or none; Reasoning is none, low, medium or high; empty
// values leave the provider default. A zero MaxTokens uses the provider's own cap.
type LLMRequest struct {
	Model      string
	Messages   []Message
	Tools      []ToolSpec
	ToolChoice string
	Reasoning  string
	MaxTokens  int
}

// LLMChunk carries text deltas, ToolCallStart once when the first tool call
// begins (text that follows it came with tool calls), then the complete tool
// calls once, then usage.
type LLMChunk struct {
	Text          string
	ToolCallStart bool
	ToolCalls     []ToolCall
	Usage         *Usage
	Err           error
}

type TTS interface {
	Open(ctx context.Context, o TTSOptions) (TTSStream, error)
}

type TTSOptions struct {
	Voice      string
	SampleRate int
	Language   string
}

// Audio carries int16 mono PCM at TTSOptions.SampleRate.
type TTSStream interface {
	Speak(text string) error
	Flush() error
	Audio() <-chan []int16
	// Err is valid once Audio is closed: nil after a clean flush or Close, else a fixed code
	Err() error
	Close() error
}

// Weather failures carry CodeWeatherFailed, or ErrPlaceNotFound for a place
// the provider does not know.
type Weather interface {
	Forecast(ctx context.Context, q WeatherQuery) (Forecast, error)
}

// ErrPlaceNotFound names no place, so it may reach any caller.
var ErrPlaceNotFound = errors.New("providers: place not found")

type WeatherQuery struct {
	Location string
	Days     int
	Language string
	// TimeZone is the household's IANA zone; empty leaves the provider's default.
	TimeZone string
}

type Forecast struct {
	Place   string
	Current *Current
	Days    []Day
}

// Current is the latest conditions, at Time (RFC 3339 in the household zone).
// Temperature is Celsius; Code and Summary use the same WMO vocabulary as Day.
type Current struct {
	Time        string
	Temperature float64
	Code        int
	Summary     string
}

// Day.Date is YYYY-MM-DD in the place's zone; Summary is English text for the
// WMO Code. A nil PrecipitationMm is not known. Days run in date order, and a
// day the provider has no code or temperatures for is left out.
type Day struct {
	Date            string
	Min, Max        float64
	PrecipitationMm *float64
	Code            int
	Summary         string
}

// Search failures carry CodeSearchFailed.
type Search interface {
	Search(ctx context.Context, query string) ([]SearchResult, error)
}

// SearchResult is one page a search found: Title and Text reach the model,
// and the rest only a screen's link card (W0-C item 10): URL (https or http),
// Site (the url's host), Published (RFC 3339 or "") and ImageURL (https or
// ""). All of it is third-party text: never logged.
type SearchResult struct {
	Title, Text          string
	URL, Site, Published string
	ImageURL             string
}

// PageText is a search provider's cleaned full text of a page it found, for
// a screen's link panel; its failures carry CodeSearchFailed. The text is
// third-party: shown, never logged.
type PageText interface {
	PageText(ctx context.Context, url string) (string, error)
}

// XSearch failures carry CodeXSearchFailed. A post is third-party data: it
// reaches the model as untrusted output and never a log.
type XSearch interface {
	SearchPosts(ctx context.Context, query string, latest bool, limit int) ([]Post, error)
}

// Post is one post X found. Time is RFC 3339 or ""; Views is 0 when X shows
// none. ReplyTo and RepostedBy are @handles; Quote is the post it quotes, and
// Note the community note X shows under it.
type Post struct {
	URL, Author, Handle, Text, Time string
	Likes, Reposts, Replies, Views  int
	ReplyTo, RepostedBy, Note       string
	Quote                           *Post
}

// Music failures carry CodeMusicFailed. A track's text is third-party data:
// it reaches the model as untrusted output and never a log.
type Music interface {
	SearchMusic(ctx context.Context, query string, limit int) ([]Track, error)
}

// Track is one song a music provider found. VideoID is its YouTube video,
// set when the provider knows it; without one, a song its own provider
// streams (a Streamer) plays from there, and any other is matched on YouTube
// Music by its artist, title and duration. ArtworkURL and WebURL (the song's
// page, to open in a browser) are https or "". Instance is the provider
// instance that found it, which the music service streams it from; the model
// never sees it. Station is the provider station whose batch it came from, so
// the queue keeps asking that station rather than the last song's radio.
// StationBatch identifies that batch in the provider's listening feedback.
type Track struct {
	Source       string
	Instance     string
	Station      string
	StationBatch string
	ID           string
	VideoID      string
	Title        string
	Artist       string
	Album        string
	DurationS    int
	ArtworkURL   string
	WebURL       string
	// Stats are the counts the provider showed beside the song (YouTube
	// Music's plays); zero where it showed none
	Stats Stats
}

// Stats are a third party's public counts of an item: numbers only, safe for
// the model and the screen, never a log. Zero is "not shown", not "none".
// Published is the upload or creation time; PublishedApprox marks one read
// from a relative text ("3 weeks ago"), good to about its unit.
type Stats struct {
	Views       int64
	Likes       int64
	Plays       int64
	Subscribers int64
	// MonthlyAudience is an artist's listeners a month (YouTube Music)
	MonthlyAudience int64
	VideoCount      int
	Published       time.Time
	PublishedApprox bool
}

// track sources, as Track.Source names them
const (
	SourceYouTube    = "youtube"
	SourceSpotify    = "spotify"
	SourceYandex     = "yandex"
	SourceZvuk       = "zvuk"
	SourceSoundCloud = "soundcloud"
	SourceVK         = "vk"
	// SourceCadence is the household's own favorites collection, not an upstream
	SourceCadence = "cadence"
)

// Streamer is a music provider that plays its own songs: it answers a stream
// of a Track it found (by its ID and Source). ErrUnplayable is a song it
// cannot play in full (a preview only, gone, blocked where the server is) and
// ErrSignInRequired one it plays only with an account or a subscription its
// secrets lack: the music service then plays the song from YouTube Music
// instead. The stream's URL is signed: it never reaches a log, the model or
// the conversation store.
type Streamer interface {
	StreamTrack(ctx context.Context, t Track) (Stream, error)
}

// TrackRadio is a music provider with its own radio of a song it found: up to
// limit songs like it, the seed left out. Its failures carry CodeMusicFailed.
type TrackRadio interface {
	TrackRadio(ctx context.Context, seed Track, limit int) ([]Track, error)
}

// TrackRating is the owner's current recommendation mark for a song. None
// clears both marks. A provider without TrackRater still gets Cadence-local
// ratings and favorites.
type TrackRating string

const (
	RatingNone    TrackRating = "none"
	RatingLike    TrackRating = "like"
	RatingDislike TrackRating = "dislike"
)

func ValidTrackRating(r TrackRating) bool {
	return r == RatingNone || r == RatingLike || r == RatingDislike
}

// TrackRater updates a song's like/dislike in the provider account that found
// it. Its failures carry CodeMusicFailed or ErrSignInRequired.
type TrackRater interface {
	RateTrack(ctx context.Context, track Track, rating TrackRating) error
}

// ListeningKind is a station event that changes recommendations.
type ListeningKind string

const (
	ListeningRadioStarted  ListeningKind = "radioStarted"
	ListeningTrackStarted  ListeningKind = "trackStarted"
	ListeningTrackFinished ListeningKind = "trackFinished"
	ListeningTrackSkipped  ListeningKind = "skip"
)

// ListeningEvent reports actual playback, not a play request. PlayedS is the
// position reached for a finish or skip; At is the event's time. Track carries
// the station and batch its provider returned.
type ListeningEvent struct {
	Track   Track
	Kind    ListeningKind
	At      time.Time
	PlayedS float64
}

// ListeningReporter sends station listening feedback to a provider. Cadence
// reports these events best-effort, in playback order per device.
type ListeningReporter interface {
	ReportListening(ctx context.Context, event ListeningEvent) error
}

// collection kinds, as Collection.Kind names them
const (
	KindAlbum     = "album"
	KindPlaylist  = "playlist"
	KindArtist    = "artist"
	KindStation   = "station"
	KindFavorites = "favorites"
)

// Collections is a music provider that also finds albums, playlists, artists
// and stations, and reads their songs. Its failures carry CodeMusicFailed,
// ErrMusicRefused or ErrSignInRequired, never upstream text.
type Collections interface {
	SearchCollections(ctx context.Context, kind, query string, limit int) ([]Collection, error)
	// CollectionSongs answers up to limit songs: an album's or a playlist's
	// in order, an artist's radio or top songs, a station's next batch
	CollectionSongs(ctx context.Context, c Collection, limit int) ([]Track, error)
}

// Collection is an album, a playlist, an artist or a station a music provider
// found. ID is the provider's own; Owner is an album's artist or a playlist's
// owner; Tracks is 0 when the provider does not say.
type Collection struct {
	Source     string
	Kind       string
	ID         string
	Title      string
	Owner      string
	Tracks     int
	ArtworkURL string
	WebURL     string
	// Radio is an artist's radio on YouTube Music, "" elsewhere
	Radio string
	// Stats are the counts the provider showed: a playlist's views, an
	// artist's subscribers or monthly audience (Plays)
	Stats Stats
}

// ErrMusicRefused is a provider refusing to read a list for this app
// (Spotify keeps its own playlists from apps without a user)
var ErrMusicRefused = errors.New("music_refused")

// Player failures carry CodePlayerFailed, ErrUnplayable or ErrSignInRequired,
// never upstream text.
type Player interface {
	// Resolve answers a stream of the YouTube video's audio that the device
	// can fetch for a few hours. Its URL is signed: it never reaches a log,
	// the model or the conversation store.
	Resolve(ctx context.Context, videoID string) (Stream, error)
}

type Stream struct {
	URL string
	// HLS marks URL as an HLS playlist (the relay rewrites its segments' urls)
	HLS bool
	// DurationS is 0 when it is not known
	DurationS int
}

var (
	// ErrUnplayable is a video that is gone, private or blocked where the
	// server is, or has no audio the device decodes.
	ErrUnplayable = errors.New("unplayable")
	// ErrSignInRequired is a video YouTube plays only to a signed-in account
	// (age, membership, or its bot check): the player's cookies would help.
	ErrSignInRequired = errors.New("sign_in_required")
)
