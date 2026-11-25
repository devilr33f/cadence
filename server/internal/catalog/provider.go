package catalog

import (
	"context"
	"errors"
	"slices"
)

// Capability is one operation a provider implements.
type Capability string

const (
	CapSearch  Capability = "search"
	CapBrowse  Capability = "browse"
	CapItem    Capability = "item"
	CapOptions Capability = "options"
	CapResolve Capability = "resolve"
	CapAct     Capability = "act"
	CapPanel   Capability = "panel"
	CapReport  Capability = "report"
)

// AllCapabilities lists every operation in Provider's order.
var AllCapabilities = []Capability{CapSearch, CapBrowse, CapItem, CapOptions, CapResolve, CapAct, CapPanel, CapReport}

// Capabilities is the subset a provider implements; the service calls nothing
// else on it, and a provider answers ErrUnsupported for the rest.
type Capabilities []Capability

func (c Capabilities) Has(op Capability) bool { return slices.Contains(c, op) }

// Provider is one Cadence provider. Every method takes the provider's own
// Keys and answers Items with Key set and Ref empty; the service issues refs.
// Failures are the sentinel errors below or a *Failure, never upstream text.
type Provider interface {
	ID() ProviderID
	Capabilities() Capabilities
	// Sources lists the provider's sources, enabled or not; none for a
	// provider without them.
	Sources(ctx context.Context) ([]SourceInfo, error)

	// Search calls emit once per source as each answers (once in all for a
	// provider without sources), with its items or its Failure, and returns
	// when every source is done. It returns an error only when nothing could
	// be asked at all; an emit error (the caller gone) stops it.
	Search(ctx context.Context, q SearchQuery, emit func(SearchBatch) error) error
	// Browse reads a feed or a node of the provider's own tree.
	Browse(ctx context.Context, r BrowseRequest) (BrowsePage, error)
	// Item reads an item's details, a page of its children, its variants,
	// panels and actions.
	Item(ctx context.Context, r ItemRequest) (ItemDetail, error)
	// Options answers the choice groups before playing, consistent with the
	// selection so far: choosing a voiceover narrows the qualities, so the
	// caller asks again with the new Selection.
	Options(ctx context.Context, r OptionsRequest) (Options, error)
	// Resolve turns an item and a selection into something to play.
	Resolve(ctx context.Context, r ResolveRequest) (Playable, error)
	// Act runs an item action (like, save, subscribe, my list, watched) and
	// answers the item's actions with their new state.
	Act(ctx context.Context, r ActRequest) ([]Action, error)
	// Panel reads one page of an item's panel (description, comments,
	// lyrics, chapters, related); the service renders it as cards.
	Panel(ctx context.Context, r PanelRequest) (PanelPage, error)
	// Report records playback progress where the provider keeps it
	// (torrplayer), so every client agrees on what was watched.
	Report(ctx context.Context, r ProgressReport) error
}

// ChoiceMemory is the remembered-choices hook: the selection last used per
// title (the title group's Key, or the item's own without one). A provider
// that keeps its own (torrplayer's sticky prefs) implements it beside
// Provider and the service uses it; otherwise the service keeps its own.
// Resolve and Options apply the remembered selection to every group a
// request leaves out, which is how "next episode, same voiceover" works.
type ChoiceMemory interface {
	Recall(ctx context.Context, title Key) (Selection, error)
	Remember(ctx context.Context, title Key, s Selection) error
}

var (
	// ErrUnsupported is an operation outside the provider's Capabilities.
	ErrUnsupported = errors.New("catalog: unsupported")
	// ErrNotFound is a key the provider no longer knows.
	ErrNotFound = errors.New("catalog: not found")
	// ErrUnplayable is a source that cannot play: no seeders, a removed
	// video, a blocked host. The caller offers another voiceover or source.
	ErrUnplayable = errors.New("catalog: unplayable")
	// ErrSignInRequired is content the provider plays only to a signed-in
	// account.
	ErrSignInRequired = errors.New("catalog: sign in required")
	// ErrRefused is an action or a read the provider refuses this account.
	ErrRefused = errors.New("catalog: refused")
)

// FailureCode is a fixed code for a failed source or call, safe for logs.
type FailureCode string

const (
	FailTimeout     FailureCode = "timeout"
	FailUnavailable FailureCode = "unavailable"
	FailAuth        FailureCode = "auth"
	FailRateLimited FailureCode = "rate_limited"
	FailFailed      FailureCode = "failed"
	// FailNoSeeders, FailRemoved and FailBlocked explain ErrUnplayable
	FailNoSeeders FailureCode = "no_seeders"
	FailRemoved   FailureCode = "removed"
	FailBlocked   FailureCode = "blocked"
)

// Failure is a provider's or a source's failure by code. Is matches
// ErrUnplayable for the unplayable codes.
type Failure struct {
	Provider ProviderID
	Source   SourceID
	Code     FailureCode
	// HTTPStatus is the upstream's answer when it gave one (502, 401, ...), 0
	// otherwise: a number, safe to log and to show
	HTTPStatus int
}

func (f *Failure) Error() string {
	s := "catalog: " + string(f.Provider)
	if f.Source != "" {
		s += "/" + string(f.Source)
	}
	return s + ": " + string(f.Code)
}

func (f *Failure) Is(target error) bool {
	switch f.Code {
	case FailNoSeeders, FailRemoved, FailBlocked:
		return target == ErrUnplayable
	}
	return false
}

// Unsupported answers ErrUnsupported for every operation; a provider embeds
// it and overrides what its Capabilities name.
type Unsupported struct{}

func (Unsupported) Sources(context.Context) ([]SourceInfo, error) { return nil, nil }
func (Unsupported) Search(context.Context, SearchQuery, func(SearchBatch) error) error {
	return ErrUnsupported
}
func (Unsupported) Browse(context.Context, BrowseRequest) (BrowsePage, error) {
	return BrowsePage{}, ErrUnsupported
}
func (Unsupported) Item(context.Context, ItemRequest) (ItemDetail, error) {
	return ItemDetail{}, ErrUnsupported
}
func (Unsupported) Options(context.Context, OptionsRequest) (Options, error) {
	return Options{}, ErrUnsupported
}
func (Unsupported) Resolve(context.Context, ResolveRequest) (Playable, error) {
	return Playable{}, ErrUnsupported
}
func (Unsupported) Act(context.Context, ActRequest) ([]Action, error) { return nil, ErrUnsupported }
func (Unsupported) Panel(context.Context, PanelRequest) (PanelPage, error) {
	return PanelPage{}, ErrUnsupported
}
func (Unsupported) Report(context.Context, ProgressReport) error { return ErrUnsupported }

// SourceInfo is one source of a provider.
type SourceInfo struct {
	ID      SourceID
	Name    string
	Enabled bool
	// Torrents says the source is a torrent indexer, not a streaming site
	Torrents bool
}

// SearchQuery is one search. Kinds narrows it (none: every kind the provider
// has); Filters are the provider's own (genre, year, season, status,
// studio), and a source that cannot apply one says so in Unenforced.
type SearchQuery struct {
	Query string
	Kinds []Kind
	// Sort is relevance (or "") or popular; a source that cannot sort says so
	// in Unenforced ("sort")
	Sort    Sort
	Filters map[string]string
	Limit   int
	// Cursor is a batch's Next, to page one provider or source further
	Cursor string
	// Sources narrows a provider with sources; none asks every enabled one
	Sources []SourceID
}

// Sort is an order a search or an item's list is asked in.
type Sort string

const (
	SortRelevance Sort = "relevance"
	SortPopular   Sort = "popular"
	SortLatest    Sort = "latest"
	SortOldest    Sort = "oldest"
	// SortUpdated is a channel's playlists by their last video added
	SortUpdated Sort = "updated"
	// SortViews orders a list by its items' views, read by the provider
	SortViews Sort = "views"
)

// well-known search filters, in SearchQuery.Filters
const (
	// FilterUploaded is today, week, month or year
	FilterUploaded = "uploaded"
	// FilterDuration is short (under 3 min), medium (3 to 20) or long
	FilterDuration = "duration"
	// FilterLive is "true" for live streams alone
	FilterLive = "live"
)

// SearchBatch is one provider's or source's answer, in the order it arrived.
type SearchBatch struct {
	Provider ProviderID
	Source   SourceID
	Items    []Item
	// Failure is set for a source that failed; Items is then empty
	Failure *Failure
	// Next pages this source further; "" at its end
	Next string
	// Unenforced names the filters this source ignored, so the list is not
	// presented as filtered
	Unenforced []string
}

// well-known Browse paths; a provider's own tree uses paths of its own below
// "tree/" (a TorrServer torrent, a folder)
const (
	FeedRoot          = ""
	FeedContinue      = "continue"
	FeedMyList        = "my_list"
	FeedNewEpisodes   = "new_episodes"
	FeedSubscriptions = "subscriptions"
	FeedLiked         = "liked"
	FeedPlaylists     = "playlists"
	FeedMixes         = "mixes"
	FeedRecommended   = "recommended"
	FeedLibrary       = "library"
	FeedCatalogue     = "catalogue"
)

type BrowseRequest struct {
	Path    string
	Filters map[string]string
	Cursor  string
	Limit   int
}

// BrowsePage is sections of items: Home's rows, or one node of a tree (one
// section).
type BrowsePage struct {
	Sections []Section
	Next     string
}

type Section struct {
	ID    string
	Title string
	Items []Item
	// Path browses the section in full
	Path string
	Next string
}

type ItemRequest struct {
	Key Key
	// Cursor pages the children; Group picks a child group ("51-100")
	Cursor string
	Group  string
	Limit  int
	// Tab picks one of ItemDetail.Tabs (a channel's videos, shorts, streams,
	// playlists); Sort orders the children where the item offers it (Sorts);
	// Query searches inside the item (a channel) or filters its children
	Tab   string
	Sort  Sort
	Query string
}

// ItemDetail is an item with what hangs off it.
type ItemDetail struct {
	Item Item
	// Children are episodes, tracks, videos, files: a page of them
	Children []Item
	Next     string
	// ChildGroups split a long list (seasons, arcs, "1-50"): ItemRequest.Group
	ChildGroups []ChildGroup
	// Variants are a title group's: one series or movie per source
	Variants []Item
	// Selected is the variant (or nothing) the remembered choices pick
	Selected *Key
	// Continue is where "continue" starts: torrplayer's NextUnwatched
	Continue *ContinueAt
	Panels   []Panel
	Actions  []Action
	// Tabs are the lists the item has (ItemRequest.Tab), the one answered
	// first; Sorts the orders this list offers, Sort the one it is in
	Tabs  []Tab
	Sorts []Sort
	Sort  Sort
	// Partial says a re-sorted list read only its first pages
	Partial bool
}

// Tab is one list of an item (a channel's videos, its playlists).
type Tab struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Selected bool   `json:"selected,omitempty"`
}

type ChildGroup struct {
	ID    string
	Label string
	Count int
}

// ContinueAt is the episode to continue and where in it.
type ContinueAt struct {
	Episode   string
	PositionS int
}

// PanelKind follows the screen protocol's MediaPanel kinds.
type PanelKind string

const (
	PanelDescription PanelKind = "description"
	PanelComments    PanelKind = "comments"
	PanelChannel     PanelKind = "channel"
	PanelLyrics      PanelKind = "lyrics"
	PanelRelated     PanelKind = "related"
	PanelChapters    PanelKind = "chapters"
	PanelCustom      PanelKind = "custom"
)

// Panel is one panel an item offers; ID is the provider's.
type Panel struct {
	ID         string
	Kind       PanelKind
	Title      string
	CountLabel string
}

type PanelRequest struct {
	Key    Key
	Panel  string
	Cursor string
}

// PanelPage is a panel's content as data; the service renders it as a
// cards.Page (long_text, post, media_tile, keyvalue) with its own action ids.
type PanelPage struct {
	Text     string
	Facts    []Fact
	Comments []Comment
	Lyrics   []LyricLine
	Chapters []Chapter
	Items    []Item
	Next     string
}

type Fact struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Comment struct {
	Author  string  `json:"author"`
	Time    string  `json:"time,omitempty"`
	Text    string  `json:"text"`
	Avatar  Artwork `json:"avatar,omitzero"`
	Likes   int     `json:"likes,omitempty"`
	Replies int     `json:"replies,omitempty"`
}

type LyricLine struct {
	AtMS int    `json:"atMs"`
	Text string `json:"text"`
}

type Chapter struct {
	StartMS int    `json:"startMs"`
	Title   string `json:"title"`
}

// ActionKind follows the screen protocol's MediaItemAction kinds, plus
// torrplayer's.
type ActionKind string

const (
	ActionLike       ActionKind = "like"
	ActionDislike    ActionKind = "dislike"
	ActionSave       ActionKind = "save"
	ActionSubscribe  ActionKind = "subscribe"
	ActionAddToQueue ActionKind = "add_to_queue"
	// ActionMyList sets a my-list status (Value: watching, planned,
	// completed, on_hold, dropped, or "" to remove)
	ActionMyList  ActionKind = "my_list"
	ActionWatched ActionKind = "watched"
	ActionCustom  ActionKind = "custom"
)

// Action is one item action and its state. ID is the provider's; the service
// issues its own ids for devices.
type Action struct {
	ID      string
	Kind    ActionKind
	Label   string
	Icon    string
	Toggled bool
	Enabled bool
	// Value is the current one (a my-list status); Values the allowed ones
	Value  string
	Values []string
}

type ActRequest struct {
	Key    Key
	Action string
	// On sets a toggle (like, save, subscribe, watched); Value sets a
	// valued action (my_list)
	On    bool
	Value string
}

// ProgressReport is a position while playing, or the end of an episode.
type ProgressReport struct {
	Key       Key
	Episode   string
	PositionS int
	DurationS int
	Watched   bool
}
