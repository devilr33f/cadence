package catalogscreen

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

const (
	// a card's text stays well inside its 16 KB encoded
	textBytes = 6 << 10
	maxFacts  = 12
)

// Panel renders a page of an item's panel as card subjects: the text in
// long_text cards (a long one split), the facts as a keyvalue, comments as
// posts, lyrics as reading text, chapters as keyvalues, related items as a
// list; Next loads the following page. Everything in it is third-party:
// shown as content, never logged.
func Panel(ctx context.Context, svc *catalog.Service, ref catalog.Ref, panel, cursor string) (*cards.Panel, error) {
	data, err := svc.Panel(ctx, catalog.Household, ref, panel, cursor)
	if err != nil {
		return nil, err
	}
	base := string(ref) + "\x1f" + panel + "\x1f" + cursor
	var subjects []cards.Subject
	add := func(build func(r *cards.Render) *cardsv1.CardBlock, fallback string) {
		if len(subjects) == pcards.MaxPageCards {
			return
		}
		subjects = append(subjects, &card{id: cards.OpaqueID("catalog", base+"#"+strconv.Itoa(len(subjects))),
			build: build, fallback: fallback})
	}
	title := func(r *cards.Render) string { return panelTitle(panel, r.Lang) }
	for _, chunk := range chunks(data.Text, textBytes) {
		add(func(r *cards.Render) *cardsv1.CardBlock {
			return &cardsv1.CardBlock{Title: title(r), Icon: "info",
				Children: pcards.Nodes(&cardsv1.LongText{Text: chunk, Reading: true})}
		}, clip(chunk, 280))
	}
	if len(data.Facts) > 0 {
		kv := &cardsv1.KeyValue{}
		for _, f := range data.Facts[:min(len(data.Facts), maxFacts)] {
			if f.Key != "" && f.Value != "" {
				kv.Items = append(kv.Items, &cardsv1.KeyValueItem{Key: clip(f.Key, 40), Value: clip(f.Value, 80)})
			}
		}
		if len(kv.Items) > 0 {
			add(func(r *cards.Render) *cardsv1.CardBlock {
				return &cardsv1.CardBlock{Title: title(r), Children: pcards.Nodes(kv)}
			}, kv.Items[0].Key+": "+kv.Items[0].Value)
		}
	}
	for i := 0; i < len(data.Comments); i += pcards.MaxListItems {
		batch := data.Comments[i:min(i+pcards.MaxListItems, len(data.Comments))]
		add(func(r *cards.Render) *cardsv1.CardBlock {
			list := &cardsv1.PagedList{}
			for _, c := range batch {
				if strings.TrimSpace(c.Text) == "" {
					continue
				}
				p := &cardsv1.Post{Author: clip(nonEmpty(c.Author, "—"), 80), Time: clip(c.Time, 40), Text: clip(c.Text, 500)}
				if c.Avatar.Hash != "" {
					p.Avatar = &cardsv1.ImageRef{Ref: "img:" + c.Avatar.Hash, Aspect: cardsv1.Aspect_ASPECT_AVATAR}
				}
				if c.Likes > 0 {
					p.Stats = append(p.Stats, &cardsv1.PostStat{Kind: cardsv1.StatKind_STAT_KIND_LIKE, Label: strconv.Itoa(c.Likes)})
				}
				if c.Replies > 0 {
					p.Stats = append(p.Stats, &cardsv1.PostStat{Kind: cardsv1.StatKind_STAT_KIND_REPLY, Label: strconv.Itoa(c.Replies)})
				}
				list.Items = append(list.Items, pcards.N(p))
			}
			return &cardsv1.CardBlock{Title: title(r), Icon: "comment", Children: pcards.Nodes(list)}
		}, clip(batch[0].Text, 280))
	}
	if len(data.Lyrics) > 0 {
		lines := make([]string, 0, len(data.Lyrics))
		for _, l := range data.Lyrics {
			lines = append(lines, l.Text)
		}
		for _, chunk := range chunks(strings.Join(lines, "\n"), textBytes) {
			add(func(r *cards.Render) *cardsv1.CardBlock {
				return &cardsv1.CardBlock{Title: title(r), Children: pcards.Nodes(&cardsv1.LongText{Text: chunk, Reading: true})}
			}, clip(chunk, 280))
		}
	}
	for i := 0; i < len(data.Chapters); i += maxFacts {
		batch := data.Chapters[i:min(i+maxFacts, len(data.Chapters))]
		kv := &cardsv1.KeyValue{}
		for _, ch := range batch {
			kv.Items = append(kv.Items, &cardsv1.KeyValueItem{Key: clock(ch.StartMS), Value: clip(nonEmpty(ch.Title, "—"), 80)})
		}
		add(func(r *cards.Render) *cardsv1.CardBlock {
			return &cardsv1.CardBlock{Title: title(r), Children: pcards.Nodes(kv)}
		}, kv.Items[0].Key+" "+kv.Items[0].Value)
	}
	for i := 0; i < len(data.Items); i += pcards.MaxListItems {
		batch := data.Items[i:min(i+pcards.MaxListItems, len(data.Items))]
		add(func(r *cards.Render) *cardsv1.CardBlock {
			list := &cardsv1.PagedList{}
			for _, it := range batch {
				line := it.Title
				if it.Subtitle != "" {
					line += " · " + it.Subtitle
				}
				list.Items = append(list.Items, pcards.N(&cardsv1.Text{Text: clip(nonEmpty(line, "—"), 200)}))
			}
			return &cardsv1.CardBlock{Title: title(r), Children: pcards.Nodes(list)}
		}, clip(nonEmpty(batch[0].Title, "—"), 280))
	}
	if len(subjects) == 0 {
		add(func(r *cards.Render) *cardsv1.CardBlock {
			return &cardsv1.CardBlock{Title: title(r), Children: pcards.Nodes(&cardsv1.Text{Text: empty(r.Lang)})}
		}, "—")
	}
	p := &cards.Panel{Cards: subjects}
	if data.Next != "" {
		next := data.Next
		p.Next = &cards.Binding{Idempotent: true, Run: func(ctx context.Context, _ cards.Act) cards.Outcome {
			np, err := Panel(ctx, svc, ref, panel, next)
			if err != nil {
				why, _ := catalog.RefusalOf(err)
				return cards.Outcome{Refused: string(why)}
			}
			return cards.Outcome{Panel: np}
		}}
	}
	return p, nil
}

// card is one rendered panel card
type card struct {
	id       string
	build    func(r *cards.Render) *cardsv1.CardBlock
	fallback string
}

func (c *card) ID() string { return c.id }

func (c *card) Render(_ context.Context, r *cards.Render) (*cardsv1.Card, error) {
	return pcards.NewCard("", 0, nonEmpty(c.fallback, "—"), c.build(r)), nil
}

var titles = map[string][2]string{
	"description": {"Описание", "Description"}, "comments": {"Комментарии", "Comments"},
	"lyrics": {"Текст песни", "Lyrics"}, "chapters": {"Главы", "Chapters"}, "related": {"Похожее", "Related"},
	"channel": {"Канал", "Channel"},
}

func panelTitle(panel, lang string) string {
	t, ok := titles[panel]
	if !ok {
		t = [2]string{"Подробнее", "More"}
	}
	if strings.HasPrefix(lang, "ru") {
		return t[0]
	}
	return t[1]
}

func empty(lang string) string {
	if strings.HasPrefix(lang, "ru") {
		return "Здесь пока ничего нет"
	}
	return "Nothing here yet"
}

func nonEmpty(s, or string) string {
	if strings.TrimSpace(s) == "" {
		return or
	}
	return s
}

// clip cuts s to n code points
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// chunks splits text at line or word ends into pieces of at most n bytes and
// 8000 code points
func chunks(text string, n int) []string {
	text = strings.TrimSpace(text)
	var out []string
	for text != "" {
		if len(text) <= n && utf8.RuneCountInString(text) <= 8000 {
			out = append(out, text)
			break
		}
		cut := n
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if i := strings.LastIndexAny(text[:cut], "\n "); i > n/2 {
			cut = i
		}
		out = append(out, strings.TrimSpace(text[:cut]))
		text = strings.TrimSpace(text[cut:])
	}
	return out
}

func clock(ms int) string {
	s := ms / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
