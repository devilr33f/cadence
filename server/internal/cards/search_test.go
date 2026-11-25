package cards_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

type pageText struct {
	text string
	err  error
	urls []string
}

func (p *pageText) PageText(_ context.Context, url string) (string, error) {
	p.urls = append(p.urls, url)
	return p.text, p.err
}

func newService() *cards.Service {
	return cards.New(cards.Deps{Lang: func(context.Context, string) string { return "ru" }})
}

func links(c *cardsv1.Card) []*cardsv1.Link {
	var out []*cardsv1.Link
	for _, n := range c.GetRoot().GetCard().GetChildren() {
		for _, it := range n.GetPagedList().GetItems() {
			if l := it.GetLink(); l != nil {
				out = append(out, l)
			}
		}
	}
	return out
}

func act(t *testing.T, s *cards.Service, dev string, c *cardsv1.Card, actionID string) calls.Reply {
	t.Helper()
	args := map[string]any{"cardId": "", "revision": 0, "actionId": actionID}
	if c != nil {
		args["cardId"], args["revision"] = c.GetId(), c.GetRevision()
	}
	b, _ := json.Marshal(args)
	return s.ActionMethod()(context.Background(), calls.Call{DeviceID: dev, ID: "r", Method: rpc.CardAction, Args: b})
}

func TestWebResultsCardAndPanel(t *testing.T) {
	s := newService()
	text := &pageText{text: "Первый абзац.\n\n\n\x07Второй\tабзац   с пробелами.\n" + strings.Repeat("слово ", 4000)}
	subj := cards.WebResults("turn-1:a", []providers.SearchResult{
		{Title: "Новости", Text: "кратко", URL: "https://news.example/a?b=1", Site: "news.example", Published: "2026-11-13T10:00:00Z"},
		{Title: "без адреса", Text: "x"},
		{Title: "Вики", Text: strings.Repeat("т", 400), URL: "https://wiki.example/" + strings.Repeat("p", 600), Site: "wiki.example"},
	}, text)
	c, err := s.Show(context.Background(), "screen-1", subj)
	if err != nil {
		t.Fatal(err)
	}
	ls := links(c)
	if len(ls) != 2 || ls[0].GetCite() != 1 || ls[1].GetCite() != 2 || ls[0].GetDomain() != "news.example" || ls[0].GetTime() != "13.11.2026" {
		t.Fatalf("links %v", ls)
	}
	if strings.Contains(c.GetId(), "turn-1") {
		t.Fatalf("card id %q is not opaque", c.GetId())
	}
	rep := act(t, s, "screen-1", c, ls[0].GetAction().GetId())
	if rep.Status != rpc.StatusOK || rep.CardAction.GetPage() == nil || len(rep.CardAction.GetPage().GetCards()) != 1 {
		t.Fatalf("panel %+v", rep)
	}
	if len(text.urls) != 1 || text.urls[0] != "https://news.example/a?b=1" {
		t.Fatalf("page text asked for %v", text.urls)
	}
	panel := rep.CardAction.GetPage().GetCards()[0]
	if err := pcards.ValidatePage(rep.CardAction.GetPage()); err != nil {
		t.Fatal(err)
	}
	kids := panel.GetRoot().GetCard().GetChildren()
	lt, qr := kids[0].GetLongText(), kids[1].GetQr()
	if lt == nil || !strings.HasPrefix(lt.GetText(), "Первый абзац.\n\nВторой абзац с пробелами.") || strings.ContainsRune(lt.GetText(), 7) {
		t.Fatalf("cleaned text %.80q", lt.GetText())
	}
	if qr.GetPayload() != "https://news.example/a?b=1" || qr.GetCaption() != "Отправить на телефон" {
		t.Fatalf("qr %v", qr)
	}
	// a link stays usable: the panel opens again
	if again := act(t, s, "screen-1", c, ls[0].GetAction().GetId()); again.Status != rpc.StatusOK {
		t.Fatalf("second open %+v", again)
	}
	// a page whose text fails shows the snippet; a url over the qr's limit gets no qr
	text.err = errors.New("down")
	cur := s2(t, s, c)
	rep = act(t, s, "screen-1", cur, links(cur)[1].GetAction().GetId())
	if rep.Status != rpc.StatusOK {
		t.Fatalf("panel %+v", rep)
	}
	kids = rep.CardAction.GetPage().GetCards()[0].GetRoot().GetCard().GetChildren()
	if len(kids) != 1 || kids[0].GetLongText().GetText() != strings.Repeat("т", 400) {
		t.Fatalf("fallback panel %v", kids)
	}
}

// s2 is the card as the service holds it now
func s2(t *testing.T, s *cards.Service, c *cardsv1.Card) *cardsv1.Card {
	t.Helper()
	cur, ok := s.Card("screen-1", c.GetId())
	if !ok {
		t.Fatal("card gone")
	}
	return cur
}

func TestPostsCard(t *testing.T) {
	s := newService()
	var posts []providers.Post
	for i := range 20 {
		posts = append(posts, providers.Post{
			URL: "https://x.com/a/status/" + strconv.Itoa(i), Author: "Автор", Handle: "@a", Text: strconv.Itoa(i) + strings.Repeat(" пост", 99),
			Time: "2026-11-13T10:00:00Z", Likes: 1234, Reposts: 2_500_000, Replies: 3, Views: 0,
			Note: "контекст", ReplyTo: "@b", RepostedBy: "@c",
			Quote: &providers.Post{Author: "Цитата", Handle: "@q", Text: strings.Repeat("ц", 400),
				Quote: &providers.Post{Author: "deep", Text: "never shown"}},
		})
	}
	posts = append(posts, providers.Post{Text: "no author"})
	c, err := s.Show(context.Background(), "screen-1", cards.Posts("turn-1:b", posts))
	if err != nil {
		t.Fatal(err)
	}
	items := c.GetRoot().GetCard().GetChildren()[0].GetPagedList().GetItems()
	if len(items) != 3 {
		t.Fatalf("%d posts", len(items))
	}
	p := items[0].GetPost()
	if p.GetSource() != "" || utf8.RuneCountInString(p.GetText()) > 181 || !strings.HasSuffix(p.GetText(), "…") || p.GetQuote().GetQuote() != nil || p.GetNote().GetText() != "контекст" || len(p.GetStats()) != 3 {
		t.Fatalf("post %v", p)
	}
	if p.GetStats()[1].GetLabel() != "2,5 млн" || p.GetStats()[2].GetLabel() != "1,2 тыс." {
		t.Fatalf("stats %v", p.GetStats())
	}
	rep := act(t, s, "screen-1", c, p.GetAction().GetId())
	if rep.Status != rpc.StatusOK || rep.CardAction.GetPage().GetCards()[0].GetRoot().GetCard().GetChildren()[1].GetQr().GetPayload() != posts[0].URL {
		t.Fatalf("post panel %+v", rep)
	}
	if cards.Posts("k", []providers.Post{{Text: "x"}}) != nil || cards.WebResults("k", nil, nil) != nil {
		t.Fatal("nothing to show must give no card")
	}
}

// a page's next action names no card: the server knows the page by its id
func TestPageNextAction(t *testing.T) {
	s := newService()
	ran := 0
	page, err := s.Page(context.Background(), "screen-1", &cards.Panel{
		Cards: []cards.Subject{cards.Posts("p1", []providers.Post{{Author: "a", Text: "one"}})},
		Next: &cards.Binding{Run: func(context.Context, cards.Act) cards.Outcome {
			ran++
			return cards.Outcome{Panel: &cards.Panel{Cards: []cards.Subject{cards.Posts("p2", []providers.Post{{Author: "b", Text: "two"}})}}}
		}},
	})
	if err != nil || page.GetNext().GetId() == "" {
		t.Fatalf("page %v %v", page, err)
	}
	if rep := act(t, s, "screen-2", nil, page.GetNext().GetId()); rep.Status != rpc.StatusNotFound {
		t.Fatalf("another device: %+v", rep)
	}
	rep := act(t, s, "screen-1", nil, page.GetNext().GetId())
	if rep.Status != rpc.StatusOK || rep.CardAction.GetPage() == nil || len(rep.CardAction.GetPage().GetCards()) != 1 || ran != 1 || rep.CardAction.GetCard() != nil {
		t.Fatalf("next %+v", rep)
	}
	if rep := act(t, s, "screen-1", nil, page.GetNext().GetId()); rep.Status != rpc.StatusNotFound {
		t.Fatalf("a next page loads once: %+v", rep)
	}
	next := rep.CardAction.GetPage()
	if next.GetId() == "" || next.GetId() == page.GetId() {
		t.Fatalf("page ids %q then %q", page.GetId(), next.GetId())
	}
}

// a page's next action names its page; another page's id is refused, and none at all is a screen's from before
// page ids
func TestPageNextActionByPageID(t *testing.T) {
	s := newService()
	panel := func() *cards.Panel {
		return &cards.Panel{
			Cards: []cards.Subject{cards.Posts("p1", []providers.Post{{Author: "a", Text: "one"}})},
			Next: &cards.Binding{Run: func(context.Context, cards.Act) cards.Outcome {
				return cards.Outcome{Panel: &cards.Panel{Cards: []cards.Subject{cards.Posts("p2", []providers.Post{{Author: "b", Text: "two"}})}}}
			}},
		}
	}
	call := func(args map[string]any) calls.Reply {
		b, _ := json.Marshal(args)
		return s.ActionMethod()(context.Background(), calls.Call{DeviceID: "screen-1", ID: "r", Method: rpc.CardAction, Args: b})
	}
	page, err := s.Page(context.Background(), "screen-1", panel())
	if err != nil {
		t.Fatal(err)
	}
	if rep := call(map[string]any{"pageId": "page.other", "actionId": page.GetNext().GetId()}); rep.Status != rpc.StatusNotFound {
		t.Fatalf("another page's id: %+v", rep)
	}
	if rep := call(map[string]any{"pageId": page.GetId(), "cardId": "c", "actionId": page.GetNext().GetId()}); rep.Status != rpc.StatusInvalid {
		t.Fatalf("a page and a card: %+v", rep)
	}
	rep := call(map[string]any{"pageId": page.GetId(), "actionId": page.GetNext().GetId()})
	if rep.Status != rpc.StatusOK || len(rep.CardAction.GetPage().GetCards()) != 1 {
		t.Fatalf("by page id: %+v", rep)
	}
}

func TestRecorderKeepsTheActionAsAToolCall(t *testing.T) {
	var got []providers.Message
	var meta conversation.TurnMeta
	rec := cards.Recorder(func(_ context.Context, dev, turn string, m conversation.TurnMeta, msgs []providers.Message) error {
		if dev != "screen-1" || !strings.HasPrefix(turn, "screen-") {
			t.Errorf("device %q turn %q", dev, turn)
		}
		meta, got = m, msgs
		return nil
	}, nil)
	rec(context.Background(), "screen-1", cards.Record{Tool: "ha_control", Args: map[string]string{"action": "toggle"}, Result: map[string]bool{"ok": true}})
	if meta.Trigger != conversation.TriggerScreen || len(got) != 3 || got[0].Content != cards.ScreenFrame ||
		got[1].ToolCalls[0].Name != "ha_control" || got[2].ToolCallID != got[1].ToolCalls[0].ID || got[2].Content != `{"ok":true}` {
		t.Fatalf("meta %+v messages %+v", meta, got)
	}
}
