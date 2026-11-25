// Package playprogress is the privacy-safe progress of a screen play, shared
// by yt-dlp, the play tools and their cards. Its events carry stages, numbers
// and codec tokens only: never a title, video id, url or upstream line. The
// card's own text (CardInfo, Item) is shown, never logged.
package playprogress

import "context"

// Stage is one step of resolving and opening a stream.
type Stage string

const (
	// a collection's songs are read before its first song resolves: an
	// album's, a playlist's or an artist's (CardInfo.Kind)
	StageCollection    Stage = "collection"
	StageCookies       Stage = "cookies"
	StageExtract       Stage = "extract"
	StageChallenge     Stage = "challenge"
	StageFormats       Stage = "formats"
	StagePick          Stage = "pick"
	StageWaitAvailable Stage = "wait_available"
	StageRelay         Stage = "relay"
	StageDone          Stage = "done"
)

// Event is a resolve stage as it happens. RemainingMS is -1 except while a
// countdown runs. Done and Failed describe the resolve, not the screen's first
// frame; a play card follows the session for that separately.
type Event struct {
	Stage       Stage  `json:"stage"`
	ElapsedMS   int64  `json:"elapsedMs"`
	RemainingMS int64  `json:"remainingMs,omitempty"`
	Height      int    `json:"height,omitempty"`
	VCodec      string `json:"vcodec,omitempty"`
	ACodec      string `json:"acodec,omitempty"`
	AudioTracks int    `json:"audioTracks,omitempty"`
	Done        bool   `json:"done,omitempty"`
	Failed      bool   `json:"failed,omitempty"`
	Code        string `json:"code,omitempty"`
}

// State is where the whole play, including its player, stands.
type State string

const (
	Resolving State = "resolving"
	Buffering State = "buffering"
	Playing   State = "playing"
	Failed    State = "failed"
)

// Update is a play card's latest state.
type Update struct {
	Event
	State            State
	SessionID        string
	BufferingPercent int // -1 when the player does not say
	// Item is the song a collection's play fetches now, nil before its list
	// is read (and for a single item, which CardInfo names)
	Item *Item
}

// Collection kinds of CardInfo.Kind
const (
	KindAlbum    = "album"
	KindPlaylist = "playlist"
	KindArtist   = "artist"
)

// CardInfo is the item and target a play card names. Kind is a collection's
// (KindAlbum, KindPlaylist, KindArtist), "" for a single item. Text is shown,
// never logged.
type CardInfo struct {
	ID, Tool, DeviceID, DeviceName string
	Title, Artist, ArtworkURL      string
	Kind                           string
	// optional play-history facts; they do not change the card (MediaKind: track, video, ...)
	ItemID, Source, Provider, MediaKind, Album string
}

// FirstStage is where a play of info starts: reading its collection, else
// resolving its item.
func FirstStage(info CardInfo) Stage {
	if info.Kind != "" {
		return StageCollection
	}
	return StageCookies
}

// Item is what a play fetches now. ArtworkRef is the image proxy's id for
// ArtworkURL, which the card's hooks fill in. Shown, never logged.
type Item struct {
	Title, Artist, ArtworkURL, ArtworkRef string
}

type itemKey struct{}

// WithItem makes the plays on ctx tell f each item they begin to fetch.
func WithItem(ctx context.Context, f func(Item)) context.Context {
	if f == nil {
		return ctx
	}
	return context.WithValue(ctx, itemKey{}, f)
}

// ReportItem tells ctx's listener, if any, that it fetches it now.
func ReportItem(ctx context.Context, it Item) {
	if f, _ := ctx.Value(itemKey{}).(func(Item)); f != nil {
		f(it)
	}
}

// Session is the server's privacy-safe view of the screen's player.
type Session struct {
	ID               string
	State            string
	ErrorCode        string
	PictureReady     bool
	BufferingPercent int
}
