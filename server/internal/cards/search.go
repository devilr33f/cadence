package cards

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// search previews (W0-C items 10 and 18): a web search's results as link
// cards numbered in result order (cite 1..n), an X search's posts as post
// cards; OK on a link answers a panel of the page's cleaned text and a qr of
// its url. Everything here is third-party text: untrusted, shown, never
// logged.

const (
	// a page panel's text stays well inside a card's 16 KB
	maxPanelBytes = 12 << 10
	qrMaxURL      = 512
	pageTimeout   = 8 * time.Second
)

// WebResults is the card of one web_search call. key names the call (its
// turn and step), so the card id is stable for it; text, when the provider
// has it, serves the link panels.
func WebResults(key string, results []providers.SearchResult, text providers.PageText) Subject {
	return WebResultsWithImages(key, results, text, nil)
}

// ImageRefs issues the image proxy's id of an image url ("" for none it
// takes): images.Proxy.Ref for a public url
type ImageRefs func(rawURL string) string

// WebResultsWithImages is WebResults with each result's image as its link's
// thumbnail, through the image proxy (a screen never sees a third-party url).
func WebResultsWithImages(key string, results []providers.SearchResult, text providers.PageText, refs ImageRefs) Subject {
	var kept []providers.SearchResult
	for _, r := range results {
		if r.URL != "" && strings.TrimSpace(r.Title) != "" {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	if len(kept) > pcards.MaxListItems {
		kept = kept[:pcards.MaxListItems]
	}
	return &webResults{key: key, results: kept, text: text, refs: refs}
}

type webResults struct {
	key     string
	results []providers.SearchResult
	text    providers.PageText
	refs    ImageRefs
}

func (w *webResults) ID() string { return OpaqueID("web", w.key) }

// ForTool: the web search's own results card
func (w *webResults) ForTool() string { return "web_search" }

func (w *webResults) Render(_ context.Context, r *Render) (*cardsv1.Card, error) {
	list := &cardsv1.PagedList{}
	for i, res := range w.results {
		l := &cardsv1.Link{
			Domain:  clip(res.Site, 64),
			Title:   clip(res.Title, 120),
			Snippet: clip(res.Text, 240),
			Time:    timeLabel(res.Published, r.Lang),
			Cite:    uint32(i + 1),
			Action:  r.Bind(w.open(res)),
		}
		if l.Domain == "" {
			l.Domain = "web"
		}
		if w.refs != nil && res.ImageURL != "" {
			if id := w.refs(res.ImageURL); id != "" {
				l.Thumbnail = &cardsv1.ImageRef{Ref: "img:" + id, Alt: clip(res.Title, 120), Aspect: cardsv1.Aspect_ASPECT_WIDE}
			}
		}
		list.Items = append(list.Items, pcards.N(l))
	}
	block := &cardsv1.CardBlock{Title: r.T("search.title"), Icon: "search", Tone: cardsv1.Tone_TONE_NEUTRAL,
		Children: pcards.Nodes(list)}
	fallback := r.T("search.fallback", "count", strconv.Itoa(len(w.results)))
	return pcards.NewCard("", 0, fallback, block), nil
}

func (w *webResults) open(res providers.SearchResult) Binding {
	return Binding{Idempotent: true, Run: func(ctx context.Context, _ Act) Outcome {
		text := ""
		if w.text != nil {
			pctx, cancel := context.WithTimeout(ctx, pageTimeout)
			t, err := w.text.PageText(pctx, res.URL)
			cancel()
			if err == nil {
				text = cleanText(t)
			}
		}
		return Outcome{Panel: &Panel{Cards: []Subject{&pagePanel{res: res, text: text}}}}
	}}
}

// pagePanel is a found page: its cleaned text and a qr to read it on a phone
type pagePanel struct {
	res  providers.SearchResult
	text string
}

func (p *pagePanel) ID() string { return OpaqueID("page", p.res.URL) }

func (p *pagePanel) Render(_ context.Context, r *Render) (*cardsv1.Card, error) {
	block := &cardsv1.CardBlock{Title: clip(p.res.Title, 80), Subtitle: clip(p.res.Site, 80), Icon: "article",
		Tone: cardsv1.Tone_TONE_NEUTRAL}
	text := p.text
	if text == "" {
		text = clip(p.res.Text, 8000)
	}
	if text == "" {
		block.Children = append(block.Children, pcards.N(&cardsv1.Text{Text: r.T("search.page_unavailable"),
			Style: cardsv1.TextStyle_TEXT_STYLE_META}))
	} else {
		block.Children = append(block.Children, pcards.N(&cardsv1.LongText{Text: text, Reading: true}))
	}
	if len(p.res.URL) <= qrMaxURL {
		block.Children = append(block.Children, pcards.N(&cardsv1.Qr{Payload: p.res.URL, Caption: r.T("search.to_phone")}))
	}
	return pcards.NewCard("", 0, clip(p.res.Title, 280), block), nil
}

// cleanText drops control characters and runs of blank lines, and cuts the
// text to what a panel card holds
func cleanText(s string) string {
	var b strings.Builder
	blank := 0
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.Map(func(r rune) rune {
			if r == '\t' {
				return ' '
			}
			if unicode.IsControl(r) || r == utf8.RuneError {
				return -1
			}
			return r
		}, line)
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			blank++
			continue
		}
		if b.Len() > 0 {
			if blank > 0 {
				b.WriteString("\n\n")
			} else {
				b.WriteByte('\n')
			}
		}
		blank = 0
		b.WriteString(line)
		if b.Len() >= maxPanelBytes {
			break
		}
	}
	return clipBytes(truncRunes(b.String(), 8000), maxPanelBytes)
}

// Posts is the card of one x_search call, key as WebResults': the posts
// keptPosts leaves, the first maxPosts.
func Posts(key string, posts []providers.Post) Subject {
	kept := keptPosts(posts)
	if len(kept) == 0 {
		return nil
	}
	return &postResults{key: key, posts: kept}
}

type postResults struct {
	key   string
	posts []providers.Post
}

func (p *postResults) ID() string { return OpaqueID("x", p.key) }

// ForTool: the X search's own posts card
func (p *postResults) ForTool() string { return "x_search" }

func (p *postResults) Render(_ context.Context, r *Render) (*cardsv1.Card, error) {
	posts := p.posts
	for {
		list := &cardsv1.PagedList{}
		for _, x := range posts {
			list.Items = append(list.Items, pcards.N(postNode(r, x, true, true)))
		}
		block := &cardsv1.CardBlock{Title: r.T("search.posts"), Icon: "reply", Tone: cardsv1.Tone_TONE_NEUTRAL,
			Children: pcards.Nodes(list)}
		c := pcards.NewCard("", 0, r.T("search.fallback", "count", strconv.Itoa(len(posts))), block)
		// many long posts are over a card's bytes: the last ones go
		if proto.Size(c) <= pcards.MaxCardBytes-1024 || len(posts) == 1 {
			return c, nil
		}
		posts = posts[:len(posts)-1]
	}
}

// postNode is a post: brief on a results card (its text cut to a few lines,
// links as their hosts, no source chip: the card's title names X), whole on
// its own panel
func postNode(r *Render, x providers.Post, top, brief bool) *cardsv1.Post {
	author := x.Author
	if author == "" {
		author = x.Handle
	}
	runes := 500
	switch {
	case brief && top:
		runes = cardPostRunes
	case brief:
		runes = cardQuoteRunes
	}
	out := &cardsv1.Post{
		Author:     clip(author, 80),
		Handle:     clip(x.Handle, 40),
		Time:       timeLabel(x.Time, r.Lang),
		Text:       cardText(x.Text, runes, r.T("search.article")),
		ReplyTo:    clip(x.ReplyTo, 40),
		RepostedBy: clip(x.RepostedBy, 40),
	}
	if !brief {
		out.Source = "X"
	}
	for _, s := range []struct {
		kind cardsv1.StatKind
		n    int
	}{{cardsv1.StatKind_STAT_KIND_REPLY, x.Replies}, {cardsv1.StatKind_STAT_KIND_REPOST, x.Reposts},
		{cardsv1.StatKind_STAT_KIND_LIKE, x.Likes}, {cardsv1.StatKind_STAT_KIND_VIEW, x.Views}} {
		if s.n > 0 {
			out.Stats = append(out.Stats, &cardsv1.PostStat{Kind: s.kind, Label: count(s.n, r.Lang)})
		}
	}
	if n := strings.TrimSpace(x.Note); n != "" {
		label := "Community note"
		if strings.HasPrefix(strings.ToLower(r.Lang), "ru") {
			label = "Примечание читателей"
		}
		out.Note = &cardsv1.Note{Label: label, Text: ellipsis(clip(n, 500), max(runes, cardPostRunes))}
	}
	if top {
		if x.Quote != nil && strings.TrimSpace(x.Quote.Text) != "" && (x.Quote.Author != "" || x.Quote.Handle != "") {
			out.Quote = postNode(r, *x.Quote, false, brief)
		}
		if x.URL != "" && len(x.URL) <= qrMaxURL {
			out.Action = r.Bind(postPanel(x))
		}
	}
	return out
}

func postPanel(x providers.Post) Binding {
	return Binding{Idempotent: true, Run: func(context.Context, Act) Outcome {
		return Outcome{Panel: &Panel{Cards: []Subject{&postPage{post: x}}}}
	}}
}

// postPage is one post on its own, with a qr of its url
type postPage struct{ post providers.Post }

func (p *postPage) ID() string { return OpaqueID("post", p.post.URL) }

func (p *postPage) Render(_ context.Context, r *Render) (*cardsv1.Card, error) {
	n := postNode(r, p.post, false, false)
	if q := p.post.Quote; q != nil && strings.TrimSpace(q.Text) != "" && (q.Author != "" || q.Handle != "") {
		n.Quote = postNode(r, *q, false, false)
	}
	block := &cardsv1.CardBlock{Title: clip(n.Author, 80), Subtitle: n.Handle, Icon: "reply", Tone: cardsv1.Tone_TONE_NEUTRAL,
		Children: pcards.Nodes(n, &cardsv1.Qr{Payload: p.post.URL, Caption: r.T("search.to_phone")})}
	return pcards.NewCard("", 0, clip(n.Author+": "+n.Text, 280), block), nil
}

// timeLabel is an RFC 3339 time as a short date, "" when it does not parse
func timeLabel(s, lang string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(lang), "ru") {
		return t.Format("02.01.2006")
	}
	return t.Format("Jan 2, 2006")
}

// count is a post's count as the screen shows it: 950, 1,2K / 1,2 тыс.
func count(n int, lang string) string {
	ru := strings.HasPrefix(strings.ToLower(lang), "ru")
	short := func(v float64, en, rus string) string {
		s := strconv.FormatFloat(v, 'f', 1, 64)
		s = strings.TrimSuffix(s, ".0")
		if ru {
			return strings.ReplaceAll(s, ".", ",") + " " + rus
		}
		return s + en
	}
	switch {
	case n >= 1_000_000:
		return short(float64(n)/1_000_000, "M", "млн")
	case n >= 1_000:
		return short(float64(n)/1_000, "K", "тыс.")
	}
	return strconv.Itoa(n)
}

// clip trims s and cuts it to n runes
func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return truncRunes(s, n)
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
