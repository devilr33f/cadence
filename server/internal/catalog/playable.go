package catalog

import "time"

// ResolveFrom says where playback starts.
type ResolveFrom string

const (
	FromStart  ResolveFrom = "start"
	FromResume ResolveFrom = "resume"
)

// DeviceProfile is what the target device plays, from its display.screen
// state: a speaker has no screen and gets audio alone.
type DeviceProfile struct {
	Screen    bool
	MaxHeight int
	// Codecs are the video codecs it decodes in hardware ("h264", "hevc")
	Codecs []string
	// SpotifyConnect names the device's Spotify Connect name, "" without one
	SpotifyConnect string
}

// Stage is a step of a visible resolve ("connecting to peers").
type Stage string

const (
	StageConnectingPeers Stage = "connecting_peers"
	StageReadingStream   Stage = "reading_stream"
	StageMatching        Stage = "matching"
	StageStarting        Stage = "starting"
)

type ResolveRequest struct {
	Key Key
	// Episode is the label of the episode to play, "" for an item without
	Episode   string
	Selection Selection
	From      ResolveFrom
	Profile   DeviceProfile
	// OnStage, when set, hears each stage as resolving reaches it
	OnStage func(Stage)
}

// PlayableKind is how a play happens, which Now Playing says.
type PlayableKind string

const (
	// PlayStream is a URL the device opens
	PlayStream PlayableKind = "stream"
	// PlayHandoff is the provider playing on the device itself (Spotify
	// Connect): the device shows a session it does not decode
	PlayHandoff PlayableKind = "handoff"
	// PlayMatch is a stream of the same song from another provider (a
	// Spotify track played from YouTube Music)
	PlayMatch PlayableKind = "match"
)

// Playable is what Resolve answers: Stream for stream and match, Handoff for
// handoff. Its URLs and headers are signed or secret: never logged, stored or
// shown to the model.
type Playable struct {
	Kind    PlayableKind
	Stream  *Stream
	Handoff *Handoff
	// MatchedBy names the provider and item a match plays from
	MatchedBy *Key
	// Selected is the selection resolved, for ChoiceMemory and the switcher
	Selected Selection
	// Episode is the label resolved, "" for an item without
	Episode string
	// Variant is the ref of the series or movie the play resolved to (a title group's chosen source), set by the
	// service's callers; providers leave it empty
	Variant Ref
}

// Stream is a URL the device can fetch for a while, with what plays beside
// it. torrplayer's URLs are behind its own proxy, reachable from the device,
// so Headers is empty for them; a provider whose URLs need headers puts them
// behind the server's relay instead, as the device cannot send them.
type Stream struct {
	URL string
	// AudioURL is a separate audio track (YouTube's adaptive formats)
	AudioURL   string
	Headers    map[string]string
	Renditions []Rendition
	Subtitles  []Subtitle
	Skips      []Skip
	ResumeMS   int64
	DurationMS int64
	Live       bool
	// Expires is when the URL stops working, zero when it does not
	Expires time.Time
	// InfoHash and FileIndex name a torrent's file, for progress and switching
	InfoHash  string
	FileIndex int
}

// Rendition is one quality of a video; the device profile picks one (TV plan
// W0-C item 6, the same shape as providers.Stream's).
type Rendition struct {
	Height   int
	FPS      int
	VCodec   string
	ACodec   string
	URL      string
	AudioURL string
	Bitrate  int
}

type Subtitle struct {
	URL      string
	Language string
	Label    string
	// Format is ass, vtt or srt
	Format  string
	Default bool
	Forced  bool
}

type SkipKind string

const (
	SkipOpening SkipKind = "opening"
	SkipEnding  SkipKind = "ending"
	SkipRecap   SkipKind = "recap"
	SkipPreview SkipKind = "preview"
)

// Skip is a skippable segment.
type Skip struct {
	Kind    SkipKind
	StartMS int64
	EndMS   int64
}

// Handoff is playback the provider runs on the device itself.
type Handoff struct {
	Provider ProviderID
	// Target is the Spotify Connect device it plays on
	Target string
	// URI is the provider's own name of what plays ("spotify:track:…")
	URI string
}
