package ytweb

import (
	"cmp"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Kind is what an Item is.
type Kind string

const (
	KindVideo    Kind = "video"
	KindShort    Kind = "short"
	KindLive     Kind = "live"
	KindChannel  Kind = "channel"
	KindPlaylist Kind = "playlist"
)

// Item is a video, a short, a live stream, a channel or a playlist YouTube
// listed. Every text is third-party: untrusted to the model, never logged.
// Stats are what the listing showed (a channel's page shows short counts, a
// search exact ones); Video reads a video's in full.
type Item struct {
	Kind  Kind
	ID    string // video id, channel id (UC…) or playlist id
	Title string
	// Channel is a video's or a playlist's owner; a channel's own handle is Handle
	Channel   string
	ChannelID string
	Handle    string
	DurationS int
	Upcoming  bool
	// Thumbnail is https on YouTube's image hosts, or ""
	Thumbnail string
	Stats     providers.Stats
}

// Page is one page of a listing; Next continues it (Client.More), "" at its end.
type Page struct {
	Items []Item
	Next  string
}

const (
	maxTitle   = 200
	maxChannel = 100
	maxDesc    = 500
)

var (
	videoIDRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	channelIDRe  = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	playlistIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{2,64}$`)
	handleRe     = regexp.MustCompile(`^@[\p{L}\p{N}._-]{1,100}$`)
)

// ValidVideoID says whether id is a YouTube video id.
func ValidVideoID(id string) bool { return videoIDRe.MatchString(id) }

// ValidChannelID says whether id is a YouTube channel id (UC…).
func ValidChannelID(id string) bool { return channelIDRe.MatchString(id) }

// ValidPlaylistID says whether id can be a playlist id.
func ValidPlaylistID(id string) bool { return playlistIDRe.MatchString(id) }

// ValidHandle says whether h is a channel handle (@name).
func ValidHandle(h string) bool { return handleRe.MatchString(h) }

// VideoThumbnail is a video's still on YouTube's image host.
func VideoThumbnail(id string) string { return "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg" }

// itemRenderers are the renderers a listing's rows come in; richItemRenderer
// wraps one of the others
var itemRenderers = map[string]bool{
	"videoRenderer": true, "gridVideoRenderer": true, "playlistVideoRenderer": true, "lockupViewModel": true,
	"shortsLockupViewModel": true, "reelItemRenderer": true, "channelRenderer": true, "gridChannelRenderer": true,
	"playlistRenderer": true, "gridPlaylistRenderer": true, "richItemRenderer": true,
}

// listing reads a browse or search answer's rows in their order (each kind
// and id once) and its continuation: the token beside the rows themselves
// wins over one a level up, which in a playlist is a dead end
func listing(root any, now time.Time) ([]Item, string) {
	var items []Item
	seen := map[string]bool{}
	var near, far string
	var visit func(v any)
	visit = func(v any) {
		switch x := v.(type) {
		case node:
			for _, k := range slices.Sorted(maps.Keys(x)) {
				if itemRenderers[k] {
					if it, ok := parseItem(k, x[k], now); ok && !seen[string(it.Kind)+it.ID] {
						seen[string(it.Kind)+it.ID] = true
						items = append(items, it)
					}
					continue
				}
				visit(x[k])
			}
		case []any:
			direct, below := false, false
			var tok string
			for _, c := range x {
				m, _ := c.(node)
				switch {
				case m == nil:
				case m["continuationItemRenderer"] != nil:
					tok = cmp.Or(tok, tokenOf(m["continuationItemRenderer"]))
				case m["continuationItemViewModel"] != nil:
					tok = cmp.Or(tok, tokenOf(m["continuationItemViewModel"]))
				case hasRow(m):
					direct = true
				case holdsRows(m):
					below = true
				}
			}
			switch {
			case tok != "" && direct && near == "":
				near = tok
			case tok != "" && below && far == "":
				far = tok
			}
			for _, c := range x {
				visit(c)
			}
		}
	}
	visit(root)
	return items, cmp.Or(near, far)
}

func hasRow(m node) bool {
	for k := range m {
		if itemRenderers[k] {
			return true
		}
	}
	return false
}

func holdsRows(v any) bool {
	found := false
	walk(v, func(k string, _ any) bool {
		if itemRenderers[k] {
			found = true
		}
		return !found
	})
	return found
}

// tokenOf is a continuation's token in any of its shapes
func tokenOf(v any) string {
	var tok string
	walk(v, func(k string, x any) bool {
		if tok != "" {
			return false
		}
		if k == "continuationCommand" {
			tok = str(x, "token")
		}
		return tok == ""
	})
	return tok
}

func parseItem(kind string, v any, now time.Time) (Item, bool) {
	m, _ := v.(node)
	if m == nil {
		return Item{}, false
	}
	switch kind {
	case "richItemRenderer":
		for k, x := range at(m, "content") {
			if itemRenderers[k] && k != "richItemRenderer" {
				return parseItem(k, x, now)
			}
		}
	case "videoRenderer", "gridVideoRenderer":
		return videoRenderer(m, now)
	case "playlistVideoRenderer":
		return playlistVideoRenderer(m, now)
	case "lockupViewModel":
		return lockup(m, now)
	case "shortsLockupViewModel":
		return shortsLockup(m, now)
	case "reelItemRenderer":
		return reelItem(m)
	case "channelRenderer", "gridChannelRenderer":
		return channelRenderer(m)
	case "playlistRenderer", "gridPlaylistRenderer":
		return playlistRenderer(m)
	}
	return Item{}, false
}

func videoRenderer(m node, now time.Time) (Item, bool) {
	id := str(m, "videoId")
	if !ValidVideoID(id) {
		return Item{}, false
	}
	it := Item{Kind: KindVideo, ID: id, Title: clip(text(m["title"]), maxTitle), Thumbnail: VideoThumbnail(id)}
	owner := anyOf(at(m, "ownerText"), at(m, "longBylineText"), at(m, "shortBylineText"))
	it.Channel, it.ChannelID = clip(text(owner), maxChannel), channelOnly(browseID(owner))
	it.DurationS, _ = parseClock(text(m["lengthText"]))
	views := text(m["viewCountText"])
	it.Stats.Views, _ = ParseCount(views)
	if strings.Contains(views, "watching") || liveBadge(m) {
		it.Kind = KindLive
	}
	if m["upcomingEventData"] != nil {
		it.Upcoming = true
	}
	publishedFrom(&it, text(m["publishedTimeText"]), now)
	return it, it.Title != ""
}

func liveBadge(m node) bool {
	live := false
	walk(m["badges"], func(k string, x any) bool {
		if k == "style" && strings.HasPrefix(str(x), "BADGE_STYLE_TYPE_LIVE_NOW") {
			live = true
		}
		return !live
	})
	walk(m["thumbnailOverlays"], func(k string, x any) bool {
		if k == "style" && str(x) == "LIVE" {
			live = true
		}
		return !live
	})
	return live
}

func playlistVideoRenderer(m node, now time.Time) (Item, bool) {
	id := str(m, "videoId")
	if !ValidVideoID(id) {
		return Item{}, false
	}
	it := Item{Kind: KindVideo, ID: id, Title: clip(text(m["title"]), maxTitle), Thumbnail: VideoThumbnail(id)}
	owner := at(m, "shortBylineText")
	it.Channel, it.ChannelID = clip(text(owner), maxChannel), channelOnly(browseID(owner))
	it.DurationS, _ = parseClock(text(m["lengthText"]))
	for _, r := range list(m, "videoInfo", "runs") {
		t := str(r, "text")
		if strings.Contains(t, "view") {
			it.Stats.Views, _ = ParseCount(t)
		} else {
			publishedFrom(&it, t, now)
		}
	}
	return it, it.Title != ""
}

// lockup is the view model YouTube now lists videos, playlists and channels
// in: rows of parts under its metadata, badges on its image
func lockup(m node, now time.Time) (Item, bool) {
	id := str(m, "contentId")
	md := at(m, "metadata", "lockupMetadataViewModel")
	title := clip(text(md["title"]), maxTitle)
	if id == "" || title == "" {
		return Item{}, false
	}
	var parts []node
	for _, row := range list(md, "metadata", "contentMetadataViewModel", "metadataRows") {
		for _, p := range list(row, "metadataParts") {
			if t := at(p, "text"); t != nil {
				parts = append(parts, t)
			}
		}
	}
	var badges []string
	walk(m["contentImage"], func(k string, x any) bool {
		if k == "thumbnailBadgeViewModel" {
			badges = append(badges, str(x, "text"))
		}
		return true
	})
	switch str(m, "contentType") {
	case "LOCKUP_CONTENT_TYPE_VIDEO":
		if !ValidVideoID(id) {
			return Item{}, false
		}
		it := Item{Kind: KindVideo, ID: id, Title: title, Thumbnail: VideoThumbnail(id)}
		for _, p := range parts {
			t := text(p)
			switch {
			case strings.Contains(t, " view") || strings.Contains(t, "watching"):
				it.Stats.Views, _ = ParseCount(t)
				if strings.Contains(t, "watching") {
					it.Kind = KindLive
				}
			case strings.Contains(t, "ago"):
				publishedFrom(&it, t, now)
			case strings.HasPrefix(t, "Scheduled for") || strings.HasPrefix(t, "Premieres"):
				it.Upcoming = true
			case it.Channel == "" && t != "":
				it.Channel, it.ChannelID = ownerName(t), channelOnly(browseID(p))
			}
		}
		if it.ChannelID == "" {
			it.ChannelID = channelOnly(str(md, "image", "decoratedAvatarViewModel", "rendererContext", "commandContext",
				"onTap", "innertubeCommand", "browseEndpoint", "browseId"))
		}
		for _, b := range badges {
			if s, ok := parseClock(b); ok {
				it.DurationS = s
			} else if strings.EqualFold(b, "LIVE") {
				it.Kind = KindLive
			}
		}
		return it, true
	case "LOCKUP_CONTENT_TYPE_PLAYLIST", "LOCKUP_CONTENT_TYPE_PODCAST":
		if !ValidPlaylistID(id) {
			return Item{}, false
		}
		it := Item{Kind: KindPlaylist, ID: id, Title: title, Thumbnail: lockupThumb(m)}
		for _, p := range parts {
			t := text(p)
			if cid := channelOnly(browseID(p)); cid != "" && it.ChannelID == "" {
				it.Channel, it.ChannelID = ownerName(t), cid
			}
			if strings.Contains(t, " view") {
				it.Stats.Views, _ = ParseCount(t)
			}
		}
		if it.Channel == "" && len(parts) > 0 {
			it.Channel = ownerName(text(parts[0]))
		}
		for _, b := range badges {
			if strings.Contains(b, "video") || strings.Contains(b, "episode") {
				n, _ := ParseCount(b)
				it.Stats.VideoCount = int(n)
			}
		}
		return it, true
	case "LOCKUP_CONTENT_TYPE_CHANNEL":
		if !ValidChannelID(id) {
			return Item{}, false
		}
		it := Item{Kind: KindChannel, ID: id, Title: title}
		for _, p := range parts {
			channelFact(&it, text(p))
		}
		return it, true
	}
	return Item{}, false
}

func lockupThumb(m node) string {
	var u string
	walk(m["contentImage"], func(k string, x any) bool {
		if u == "" && k == "sources" {
			if l := list(x); len(l) > 0 {
				u = safeImage(str(l[len(l)-1], "url"))
			}
		}
		return u == ""
	})
	return u
}

func shortsLockup(m node, now time.Time) (Item, bool) {
	id := str(m, "onTap", "innertubeCommand", "reelWatchEndpoint", "videoId")
	if !ValidVideoID(id) {
		id = strings.TrimPrefix(str(m, "entityId"), "shorts-shelf-item-")
	}
	if !ValidVideoID(id) {
		return Item{}, false
	}
	it := Item{Kind: KindShort, ID: id, Title: clip(text(at(m, "overlayMetadata", "primaryText")), maxTitle),
		Thumbnail: VideoThumbnail(id)}
	sec := text(at(m, "overlayMetadata", "secondaryText"))
	if sec == "" {
		sec = str(m, "accessibilityText")
	}
	it.Stats.Views, _ = countBefore(sec, " view")
	publishedFrom(&it, sec, now)
	return it, it.Title != ""
}

func reelItem(m node) (Item, bool) {
	id := str(m, "videoId")
	if !ValidVideoID(id) {
		return Item{}, false
	}
	it := Item{Kind: KindShort, ID: id, Title: clip(text(m["headline"]), maxTitle), Thumbnail: VideoThumbnail(id)}
	it.Stats.Views, _ = ParseCount(text(m["viewCountText"]))
	return it, it.Title != ""
}

func channelRenderer(m node) (Item, bool) {
	id := str(m, "channelId")
	if !ValidChannelID(id) {
		return Item{}, false
	}
	it := Item{Kind: KindChannel, ID: id, Title: clip(text(m["title"]), maxChannel)}
	// the fields' names lie: subscriberCountText holds the handle and
	// videoCountText the subscribers, so each is read by what it says
	for _, k := range []string{"subscriberCountText", "videoCountText"} {
		channelFact(&it, text(m[k]))
	}
	if it.Handle == "" {
		if h := strings.TrimPrefix(str(m, "navigationEndpoint", "browseEndpoint", "canonicalBaseUrl"), "/"); ValidHandle(h) {
			it.Handle = h
		}
	}
	if l := list(m, "thumbnail", "thumbnails"); len(l) > 0 {
		it.Thumbnail = safeImage(str(l[len(l)-1], "url"))
	}
	return it, it.Title != ""
}

// channelFact reads one of a channel's lines: its handle, subscribers or videos
func channelFact(it *Item, t string) {
	switch {
	case ValidHandle(t):
		it.Handle = t
	case strings.Contains(t, "subscriber"):
		it.Stats.Subscribers, _ = ParseCount(t)
	case strings.Contains(t, "video"):
		n, _ := ParseCount(t)
		it.Stats.VideoCount = int(n)
	}
}

func playlistRenderer(m node) (Item, bool) {
	id := str(m, "playlistId")
	if !ValidPlaylistID(id) {
		return Item{}, false
	}
	it := Item{Kind: KindPlaylist, ID: id, Title: clip(text(m["title"]), maxTitle)}
	owner := anyOf(at(m, "longBylineText"), at(m, "shortBylineText"))
	it.Channel, it.ChannelID = ownerName(text(owner)), channelOnly(browseID(owner))
	n, ok := ParseCount(str(m, "videoCount"))
	if !ok {
		n, _ = ParseCount(text(m["videoCountText"]))
	}
	it.Stats.VideoCount = int(n)
	if l := list(m, "thumbnails"); len(l) > 0 {
		if t := list(l[0], "thumbnails"); len(t) > 0 {
			it.Thumbnail = safeImage(str(t[len(t)-1], "url"))
		}
	}
	return it, it.Title != ""
}

func publishedFrom(it *Item, t string, now time.Time) {
	if at, ok := parseAgo(t, now); ok {
		it.Stats.Published, it.Stats.PublishedApprox = at, true
	}
}

// ownerName is a playlist row's owner, without the labels YouTube puts
// beside it ("by X", "X · Playlist"); "" for a label alone
func ownerName(t string) string {
	t = strings.TrimPrefix(strings.TrimSpace(t), "by ")
	if i := strings.Index(t, " · "); i >= 0 {
		t = t[:i]
	}
	switch {
	case t == "Playlist", t == "Podcast", t == "Mix", t == "Course", t == "View full playlist", strings.HasPrefix(t, "Updated"):
		return ""
	}
	return clip(t, maxChannel)
}

func channelOnly(id string) string {
	if ValidChannelID(id) {
		return id
	}
	return ""
}

// safeImage keeps an image on YouTube's own hosts, over https
func safeImage(u string) string {
	if strings.HasPrefix(u, "//") {
		u = "https:" + u
	}
	if len(u) > 1024 || !strings.HasPrefix(u, "https://") {
		return ""
	}
	host := strings.SplitN(strings.TrimPrefix(u, "https://"), "/", 2)[0]
	if host == "i.ytimg.com" || strings.HasSuffix(host, ".ytimg.com") || strings.HasSuffix(host, ".ggpht.com") ||
		strings.HasSuffix(host, ".googleusercontent.com") {
		return u
	}
	return ""
}
