package ytweb

import (
	"context"
	"encoding/base64"
	"strings"
)

// search filter values: YouTube.js's SearchFilter (protos/misc/params.proto),
// checked live 2026-11-16
var (
	searchSorts    = map[string]byte{"relevance": 0, "popular": 3}
	searchUploaded = map[string]byte{"today": 2, "week": 3, "month": 4, "year": 5}
	searchTypes    = map[string]byte{"video": 1, "channel": 2, "playlist": 3, "movie": 4, "short": 9}
	// short is under 3 minutes, medium 3 to 20, long over 20
	searchDurations = map[string]byte{"short": 4, "medium": 5, "long": 2}
)

// SearchOptions is one search. Type, Sort, Uploaded and Duration take the
// keys of the maps above; an unknown value is refused (BadOption), "" is any.
type SearchOptions struct {
	Query    string
	Type     string
	Sort     string
	Uploaded string
	Duration string
	// Live keeps live streams alone
	Live bool
}

// BadOption is a search option outside its set.
type BadOption struct{ Name string }

func (e *BadOption) Error() string { return "ytweb: bad option " + e.Name }

// params is the search's filter protobuf, base64: field 1 the sort, field 2
// the filters (1 upload date, 2 type, 3 duration, 8 live)
func (o SearchOptions) params() (string, error) {
	pick := func(name, v string, m map[string]byte) (byte, error) {
		if v == "" {
			return 0, nil
		}
		b, ok := m[v]
		if !ok {
			return 0, &BadOption{Name: name}
		}
		return b, nil
	}
	sort, err := pick("sort", o.Sort, searchSorts)
	if err != nil {
		return "", err
	}
	up, err := pick("uploaded", o.Uploaded, searchUploaded)
	if err != nil {
		return "", err
	}
	typ, err := pick("type", o.Type, searchTypes)
	if err != nil {
		return "", err
	}
	dur, err := pick("duration", o.Duration, searchDurations)
	if err != nil {
		return "", err
	}
	var f []byte
	if up != 0 {
		f = append(f, 0x08, up)
	}
	if typ != 0 {
		f = append(f, 0x10, typ)
	}
	if dur != 0 {
		f = append(f, 0x18, dur)
	}
	if o.Live {
		f = append(f, 0x40, 1)
	}
	var b []byte
	if sort != 0 {
		b = append(b, 0x08, sort)
	}
	if len(f) > 0 {
		// f holds at most four two-byte fields, so one length byte always fits
		b = append(b, 0x12, byte(len(f))) //nolint:gosec // four two-byte fields, length fits a byte
		b = append(b, f...)
	}
	if len(b) == 0 {
		return "", nil
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Search answers the first page of a search, in YouTube's order: videos,
// shorts, channels and playlists as it mixes them, or one Type.
func (c *Client) Search(ctx context.Context, o SearchOptions) (Page, error) {
	var page Page
	err := c.guard("search", func() error {
		q := strings.Join(strings.Fields(o.Query), " ")
		if q == "" {
			return nil
		}
		params, err := o.params()
		if err != nil {
			return err
		}
		body := map[string]any{"query": q}
		if params != "" {
			body["params"] = params
		}
		root, err := c.post(ctx, "search", body)
		if err != nil {
			return err
		}
		page = c.page(root, "search", Item{})
		return nil
	})
	return page, err
}

// More answers a listing's next page by a Page's Next.
func (c *Client) More(ctx context.Context, next string) (Page, error) {
	var page Page
	err := c.guard("more", func() error {
		cur, ok := c.cursors.get(next)
		if !ok {
			return ErrBadCursor
		}
		root, err := c.post(ctx, cur.endpoint, map[string]any{"continuation": cur.token})
		if err != nil {
			return err
		}
		page = c.page(root, cur.endpoint, cur.fill)
		return nil
	})
	return page, err
}

// page reads a listing and keeps its continuation behind a cursor; fill
// names the owner of rows that leave theirs out (a channel's own tabs)
func (c *Client) page(root node, endpoint string, fill Item) Page {
	items, tok := listing(root, c.now())
	for i := range items {
		if items[i].Kind == KindChannel || fill.ID == "" {
			continue
		}
		if items[i].Channel == "" {
			items[i].Channel = fill.Title
		}
		if items[i].ChannelID == "" {
			items[i].ChannelID = fill.ID
		}
	}
	if items == nil {
		items = []Item{}
	}
	return Page{Items: items, Next: c.cursors.add(cursor{endpoint: endpoint, token: tok, fill: fill})}
}
