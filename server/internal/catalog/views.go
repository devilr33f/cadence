package catalog

import "time"

// the service's answers as JSON: the TV's RPC results and the admin API's
// bodies. Items carry refs, never keys; artwork an image id, never a url.

// SourceView is one source of a provider as a search names it.
type SourceView struct {
	Provider ProviderID `json:"provider"`
	Source   SourceID   `json:"source,omitempty"`
	Name     string     `json:"name"`
	Enabled  bool       `json:"enabled"`
	Torrents bool       `json:"torrents,omitempty"`
}

// ProviderView is one configured provider.
type ProviderView struct {
	ID ProviderID `json:"id"`
	// Instance is the provider instance it is built from
	Instance     string       `json:"instance"`
	Capabilities []Capability `json:"capabilities" nullable:"false"`
	Sources      []SourceView `json:"sources" nullable:"false"`
	// Failure is the code its sources could not be read with, "" when they were
	Failure FailureCode `json:"failure,omitempty"`
}

// FailureView is a failed provider or source, by code.
type FailureView struct {
	Code FailureCode `json:"code"`
	// HTTPStatus is the upstream's answer when it gave one (the screen shows it as it is)
	HTTPStatus int `json:"httpStatus,omitempty"`
}

// SearchEvent is one step of a streamed search. The first carries Pending,
// every source asked; each later one a provider's or a source's answer:
// its new items (title groups among them), the title groups it changed that
// an earlier event sent (replace them by ref, in place), or its failure.
// Done marks the last.
type SearchEvent struct {
	Pending    []SourceView `json:"pending,omitempty"`
	Provider   ProviderID   `json:"provider,omitempty"`
	Source     SourceID     `json:"source,omitempty"`
	Items      []Item       `json:"items,omitempty"`
	Updated    []Item       `json:"updated,omitempty"`
	Failure    *FailureView `json:"failure,omitempty"`
	Next       string       `json:"next,omitempty"`
	Unenforced []string     `json:"unenforced,omitempty"`
	Done       bool         `json:"done,omitempty"`
}

// SectionView is one row of a page.
type SectionView struct {
	ID       string     `json:"id"`
	Provider ProviderID `json:"provider"`
	Title    string     `json:"title,omitempty"`
	Items    []Item     `json:"items" nullable:"false"`
	// Path browses the section in full
	Path string `json:"path,omitempty"`
	Next string `json:"next,omitempty"`
}

// PageView is a browse answer.
type PageView struct {
	Sections []SectionView `json:"sections" nullable:"false"`
	Next     string        `json:"next,omitempty"`
}

type ChildGroupView struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type ContinueView struct {
	Episode   string `json:"episode"`
	PositionS int    `json:"positionS,omitempty"`
}

type PanelView struct {
	ID         string    `json:"id"`
	Kind       PanelKind `json:"kind"`
	Title      string    `json:"title"`
	CountLabel string    `json:"countLabel,omitempty"`
}

// ActionView is an item action by an id the service issued for it.
type ActionView struct {
	ID      string     `json:"id"`
	Kind    ActionKind `json:"kind"`
	Label   string     `json:"label"`
	Icon    string     `json:"icon,omitempty"`
	Toggled bool       `json:"toggled"`
	Enabled bool       `json:"enabled"`
	Value   string     `json:"value,omitempty"`
	Values  []string   `json:"values,omitempty"`
}

// DetailView is an item read.
type DetailView struct {
	Item        Item             `json:"item"`
	Children    []Item           `json:"children" nullable:"false"`
	Next        string           `json:"next,omitempty"`
	ChildGroups []ChildGroupView `json:"childGroups,omitempty"`
	Variants    []Item           `json:"variants,omitempty"`
	// Selected is the variant the remembered choices pick
	Selected Ref           `json:"selected,omitempty"`
	Continue *ContinueView `json:"continue,omitempty"`
	Panels   []PanelView   `json:"panels" nullable:"false"`
	Actions  []ActionView  `json:"actions" nullable:"false"`
	// Tabs are the item's lists (a channel's videos, shorts, streams,
	// playlists); Sorts the orders this list offers and Sort the one it is in
	Tabs  []Tab  `json:"tabs,omitempty"`
	Sorts []Sort `json:"sorts,omitempty"`
	Sort  Sort   `json:"sort,omitempty"`
	// Partial says a re-sorted list read only its first pages
	Partial bool `json:"partial,omitempty"`
}

type StreamSourceView struct {
	Translator string    `json:"translator,omitempty"`
	TransType  TransType `json:"transType,omitempty"`
	Language   string    `json:"language,omitempty"`
	Quality    int       `json:"quality,omitempty"`
}

type TorrentSourceView struct {
	Episodes  string `json:"episodes,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	Seeders   int    `json:"seeders"`
	Leechers  int    `json:"leechers,omitempty"`
	Quality   int    `json:"quality,omitempty"`
	Codec     string `json:"codec,omitempty"`
	Hardsub   bool   `json:"hardsub,omitempty"`
}

type ChoiceView struct {
	ID          string             `json:"id"`
	Label       string             `json:"label"`
	Detail      string             `json:"detail,omitempty"`
	Badges      []Badge            `json:"badges,omitempty"`
	Unavailable bool               `json:"unavailable,omitempty"`
	Reason      FailureCode        `json:"reason,omitempty"`
	Stream      *StreamSourceView  `json:"stream,omitempty"`
	Torrent     *TorrentSourceView `json:"torrent,omitempty"`
}

type ChoiceGroupView struct {
	Kind    GroupKind    `json:"kind"`
	Label   string       `json:"label"`
	Choices []ChoiceView `json:"choices" nullable:"false"`
	Hidden  int          `json:"hidden,omitempty"`
}

// OptionsView is an item's choice groups for one episode.
type OptionsView struct {
	Groups []ChoiceGroupView `json:"groups" nullable:"false"`
	// Selected is what resolve would play now
	Selected Selection `json:"selected" nullable:"false"`
	// Remembered marks the groups whose choice came from the remembered one
	Remembered []GroupKind `json:"remembered,omitempty"`
}

type SubtitleView struct {
	URL      string `json:"url"`
	Language string `json:"language,omitempty"`
	Label    string `json:"label,omitempty"`
	Format   string `json:"format,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
}

type SkipView struct {
	Kind    SkipKind `json:"kind"`
	StartMS int64    `json:"startMs"`
	EndMS   int64    `json:"endMs"`
}

type RenditionView struct {
	Height   int    `json:"height"`
	FPS      int    `json:"fps,omitempty"`
	VCodec   string `json:"vcodec,omitempty"`
	URL      string `json:"url"`
	AudioURL string `json:"audioUrl,omitempty"`
}

// StreamView is what a device opens; its urls are signed: never logged.
type StreamView struct {
	URL        string          `json:"url"`
	AudioURL   string          `json:"audioUrl,omitempty"`
	Renditions []RenditionView `json:"renditions,omitempty"`
	Subtitles  []SubtitleView  `json:"subtitles,omitempty"`
	Skips      []SkipView      `json:"skips,omitempty"`
	ResumeMS   int64           `json:"resumeMs,omitempty"`
	DurationMS int64           `json:"durationMs,omitempty"`
	Live       bool            `json:"live,omitempty"`
	Expires    *time.Time      `json:"expires,omitempty"`
}

type HandoffView struct {
	Provider ProviderID `json:"provider"`
	Target   string     `json:"target"`
}

// PlayableView is a resolve's answer.
type PlayableView struct {
	Kind      PlayableKind `json:"kind"`
	Stream    *StreamView  `json:"stream,omitempty"`
	Handoff   *HandoffView `json:"handoff,omitempty"`
	MatchedBy Ref          `json:"matchedBy,omitempty"`
	Selected  Selection    `json:"selected,omitempty"`
	Episode   string       `json:"episode,omitempty"`
	// Ref is the variant played, for progress reports
	Ref Ref `json:"ref"`
}

// PanelData is a panel page as data (the admin API's); devices get cards.
type PanelData struct {
	Text     string      `json:"text,omitempty"`
	Facts    []Fact      `json:"facts,omitempty"`
	Comments []Comment   `json:"comments,omitempty"`
	Lyrics   []LyricLine `json:"lyrics,omitempty"`
	Chapters []Chapter   `json:"chapters,omitempty"`
	Items    []Item      `json:"items,omitempty"`
	Next     string      `json:"next,omitempty"`
}

func choiceView(c Choice) ChoiceView {
	v := ChoiceView{ID: c.ID, Label: c.Label, Detail: c.Detail, Badges: c.Badges, Unavailable: c.Unavailable, Reason: c.Reason}
	if c.Stream != nil {
		v.Stream = &StreamSourceView{Translator: c.Stream.Translator, TransType: c.Stream.TransType,
			Language: c.Stream.Language, Quality: c.Stream.Quality}
	}
	if c.Torrent != nil {
		t := TorrentSourceView(*c.Torrent)
		v.Torrent = &t
	}
	return v
}

func streamView(s *Stream) *StreamView {
	if s == nil {
		return nil
	}
	v := &StreamView{URL: s.URL, AudioURL: s.AudioURL, ResumeMS: s.ResumeMS, DurationMS: s.DurationMS, Live: s.Live}
	for _, r := range s.Renditions {
		v.Renditions = append(v.Renditions, RenditionView{Height: r.Height, FPS: r.FPS, VCodec: r.VCodec, URL: r.URL,
			AudioURL: r.AudioURL})
	}
	for _, sub := range s.Subtitles {
		v.Subtitles = append(v.Subtitles, SubtitleView(sub))
	}
	for _, sk := range s.Skips {
		v.Skips = append(v.Skips, SkipView(sk))
	}
	if !s.Expires.IsZero() {
		at := s.Expires.UTC()
		v.Expires = &at
	}
	return v
}
