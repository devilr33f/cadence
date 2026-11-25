package ytweb

import (
	"context"
	"net/url"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Tab is a channel's list.
type Tab string

const (
	TabVideos    Tab = "videos"
	TabShorts    Tab = "shorts"
	TabStreams   Tab = "streams"
	TabPlaylists Tab = "playlists"
)

// tabParams are the browse params of a channel's tabs (Yattee Server's,
// checked live 2026-11-16)
var tabParams = map[Tab]string{
	TabVideos:    "EgZ2aWRlb3PyBgQKAjoA",
	TabShorts:    "EgZzaG9ydHPyBgUKA5oBAA==",
	TabStreams:   "EgdzdHJlYW1z8gYECgJ6AA==",
	TabPlaylists: "EglwbGF5bGlzdHPyBgQKAkIA",
}

// Sort is a channel tab's order: latest, popular and oldest for its videos,
// shorts and streams; latest (date created) and updated (last video added)
// for its playlists.
type Sort string

const (
	SortLatest  Sort = "latest"
	SortPopular Sort = "popular"
	SortOldest  Sort = "oldest"
	SortUpdated Sort = "updated"
)

// sortLabels are the menu labels each sort is offered under (hl=en)
var sortLabels = map[Sort][]string{
	SortLatest:  {"Latest", "Date added (newest)", "Newest"},
	SortPopular: {"Popular", "Most popular"},
	SortOldest:  {"Oldest", "Date added (oldest)"},
	SortUpdated: {"Last video added"},
}

// Channel is a channel's header.
type Channel struct {
	ID          string
	Title       string
	Handle      string
	Description string
	Thumbnail   string
	// Stats: Subscribers and VideoCount
	Stats providers.Stats
}

// ChannelPage is one tab of a channel: its header, the first page of the
// list and the sort it is in ("" where the tab offers none, or not the one
// asked for).
type ChannelPage struct {
	Channel Channel
	Page    Page
	Sort    Sort
	// Sorts are the orders this tab offers
	Sorts []Sort
}

// ResolveHandle answers a handle's channel id (@name → UC…); the browse
// endpoint takes no handle.
func (c *Client) ResolveHandle(ctx context.Context, handle string) (string, error) {
	var id string
	err := c.guard("resolve", func() error {
		var err error
		id, err = c.resolve(ctx, handle)
		return err
	})
	return id, err
}

func (c *Client) resolve(ctx context.Context, handle string) (string, error) {
	if !ValidHandle(handle) {
		return "", ErrNotFound
	}
	if id, ok := c.handles.get(handle); ok {
		return id, nil
	}
	root, err := c.post(ctx, "navigation/resolve_url", map[string]any{"url": "https://www.youtube.com/" + url.PathEscape(handle)})
	if err != nil {
		return "", err
	}
	id := str(root, "endpoint", "browseEndpoint", "browseId")
	if !ValidChannelID(id) {
		return "", ErrNotFound
	}
	c.handles.put(handle, id)
	return id, nil
}

// channelID takes a channel id or a handle
func (c *Client) channelID(ctx context.Context, idOrHandle string) (string, error) {
	switch {
	case ValidChannelID(idOrHandle):
		return idOrHandle, nil
	case ValidHandle(idOrHandle):
		return c.resolve(ctx, idOrHandle)
	}
	return "", ErrNotFound
}

// ChannelTab reads a channel's tab (videos when tab is ""), in sort when the
// tab offers it ("" keeps the tab's own: latest).
func (c *Client) ChannelTab(ctx context.Context, idOrHandle string, tab Tab, sort Sort) (ChannelPage, error) {
	var out ChannelPage
	err := c.guard("channel", func() error {
		id, err := c.channelID(ctx, idOrHandle)
		if err != nil {
			return err
		}
		if tab == "" {
			tab = TabVideos
		}
		params, ok := tabParams[tab]
		if !ok {
			return &BadOption{Name: "tab"}
		}
		root, err := c.post(ctx, "browse", map[string]any{"browseId": id, "params": params})
		if err != nil {
			return err
		}
		out.Channel = channelHeader(root, id)
		menu := sortMenu(root)
		for s := range menu {
			out.Sorts = append(out.Sorts, s)
		}
		sortSorts(out.Sorts)
		out.Sort = selectedSort(root)
		fill := Item{Title: out.Channel.Title, ID: out.Channel.ID}
		if sort != "" && sort != out.Sort {
			if m, ok := menu[sort]; ok {
				if root, err = c.post(ctx, "browse", m.body()); err != nil {
					return err
				}
				out.Sort = sort
			}
		}
		out.Page = c.page(root, "browse", fill)
		return nil
	})
	return out, err
}

// ChannelSearch searches inside a channel: its videos and playlists that
// match query, as the channel's own search box finds them.
func (c *Client) ChannelSearch(ctx context.Context, idOrHandle, query string) (ChannelPage, error) {
	var out ChannelPage
	err := c.guard("channel_search", func() error {
		id, err := c.channelID(ctx, idOrHandle)
		if err != nil {
			return err
		}
		q := strings.Join(strings.Fields(query), " ")
		if q == "" {
			return &BadOption{Name: "query"}
		}
		root, err := c.post(ctx, "browse", map[string]any{"browseId": id, "params": channelSearchParams, "query": q})
		if err != nil {
			return err
		}
		out.Channel = channelHeader(root, id)
		out.Page = c.page(root, "browse", Item{Title: out.Channel.Title, ID: out.Channel.ID})
		return nil
	})
	return out, err
}

// channelSearchParams opens a channel's search tab (its expandableTabRenderer)
const channelSearchParams = "EgZzZWFyY2jyBgQKAloA"

func channelHeader(root node, id string) Channel {
	ch := Channel{ID: id}
	md := at(root, "metadata", "channelMetadataRenderer")
	if ext := str(md, "externalId"); ValidChannelID(ext) {
		ch.ID = ext
	}
	ch.Title = clip(str(md, "title"), maxChannel)
	ch.Description = clip(str(md, "description"), maxDesc)
	if l := list(md, "avatar", "thumbnails"); len(l) > 0 {
		ch.Thumbnail = safeImage(str(l[len(l)-1], "url"))
	}
	ph := at(root, "header", "pageHeaderRenderer", "content", "pageHeaderViewModel")
	if ch.Title == "" {
		ch.Title = clip(text(at(ph, "title", "dynamicTextViewModel", "text")), maxChannel)
	}
	var facts Item
	walk(at(ph, "metadata"), func(k string, x any) bool {
		if k == "content" {
			channelFact(&facts, str(x))
		}
		return true
	})
	ch.Stats, ch.Handle = facts.Stats, facts.Handle
	if ch.Handle == "" {
		if u := str(md, "vanityChannelUrl"); u != "" {
			if h := u[strings.LastIndex(u, "/")+1:]; ValidHandle(h) {
				ch.Handle = h
			}
		}
	}
	return ch
}

// sortChoice is how one sort is reached: a continuation (the videos tab's
// menu) or a browse with other params (the playlists tab's)
type sortChoice struct {
	token    string
	browseID string
	params   string
}

func (s sortChoice) body() map[string]any {
	if s.token != "" {
		return map[string]any{"continuation": s.token}
	}
	return map[string]any{"browseId": s.browseID, "params": s.params}
}

func sortOf(label string) Sort {
	for s, ls := range sortLabels {
		for _, l := range ls {
			if strings.EqualFold(strings.TrimSpace(label), l) {
				return s
			}
		}
	}
	return ""
}

// sortMenu is the sorts a tab offers: the chip bar's drop-down (its list
// items), older chips, or the playlists tab's sort menu
func sortMenu(root node) map[Sort]sortChoice {
	out := map[Sort]sortChoice{}
	walk(root, func(k string, x any) bool {
		switch k {
		case "listItemViewModel":
			if s := sortOf(text(at(x, "title"))); s != "" {
				if tok := tokenOf(x); tok != "" {
					out[s] = sortChoice{token: tok}
				}
			}
			return false
		case "chipCloudChipRenderer":
			if s := sortOf(text(at(x, "text"))); s != "" {
				if tok := tokenOf(x); tok != "" {
					out[s] = sortChoice{token: tok}
				}
			}
			return false
		case "sortFilterSubMenuRenderer":
			for _, it := range list(x, "subMenuItems") {
				s := sortOf(str(it, "title"))
				be := at(it, "navigationEndpoint", "browseEndpoint")
				if s != "" && str(be, "browseId") != "" {
					out[s] = sortChoice{browseID: str(be, "browseId"), params: str(be, "params")}
				} else if tok := tokenOf(it); s != "" && tok != "" {
					out[s] = sortChoice{token: tok}
				}
			}
			return false
		}
		return true
	})
	return out
}

// selectedSort is the sort a tab answered in
func selectedSort(root node) Sort {
	var sel Sort
	walk(root, func(k string, x any) bool {
		if sel != "" {
			return false
		}
		switch k {
		case "listItemViewModel":
			if b, _ := at(x)["isSelected"].(bool); b {
				sel = sortOf(text(at(x, "title")))
			}
			return false
		case "sortFilterSubMenuRenderer":
			for _, it := range list(x, "subMenuItems") {
				if b, _ := at(it)["selected"].(bool); b {
					sel = sortOf(str(it, "title"))
				}
			}
			return false
		}
		return true
	})
	return sel
}

func sortSorts(s []Sort) {
	order := map[Sort]int{SortLatest: 0, SortPopular: 1, SortOldest: 2, SortUpdated: 3}
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && order[s[j]] < order[s[j-1]]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
