package cards_test

import (
	"context"
	"testing"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"

	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// the owner's screenshots: a promo in capitals with emoji bullets, a bare
// link, an X article as a raw url, the same text twice
var foundPosts = []providers.Post{
	{URL: "https://x.com/spam/status/1", Author: "LockedOnSol21", Handle: "@Clementene21", Time: "2026-11-15T08:00:00Z",
		Text: "✅ CLAUDE ULTRA | SUCCESSFUL IDENTITY VERIFICATION ✅ Another identity verification process successfully completed " +
			"through Persona! 🔥 🔥 CLAUDE ULTRA — AI SUBSCRIPTION SERVICES ✅ Persona Identity Verification Completed ✅ " +
			"Dedicated Onboarding ✅ Business & Bulk Procurement Inquiries", Replies: 1, Likes: 1, Views: 105},
	{URL: "https://x.com/promo/status/2", Author: "Deals", Handle: "@deals", Text: "Claude Pro for cheap, DM me for the promo code"},
	{URL: "https://x.com/link/status/3", Author: "Linker", Handle: "@linker", Text: "https://example.com/some/page"},
	{URL: "https://x.com/spydxdte/status/4", Author: "Alex Veliz", Handle: "@spydxdte", Time: "2026-11-13T10:00:00Z",
		Text: "https://x.com/i/article/2121369083069284352", Views: 177},
	{URL: "https://x.com/news/status/5", Author: "Tech news", Handle: "@technews", Time: "2026-11-14T09:00:00Z",
		Text: "Anthropic is said to weigh an IPO next year, sources say https://example.com/ipo-story", Likes: 2400, Views: 98000},
	{URL: "https://x.com/copy/status/6", Author: "Copier", Handle: "@copier",
		Text: "Anthropic is said to weigh an IPO next year, sources say https://other.example/x"},
	{URL: "https://x.com/dev/status/7", Author: "Dev", Handle: "@dev", Text: "Claude Code 刚刚发布了团队技能 🎉 チーム向けのスキル"},
	{URL: "https://x.com/late/status/8", Author: "Late", Handle: "@late", Text: "the fourth one, never on the card"},
}

func TestPostsCardKeepsWhatIsWorthReading(t *testing.T) {
	c, err := newService().Show(context.Background(), "screen-1", cards.Posts("turn-1:x", foundPosts))
	if err != nil {
		t.Fatal(err)
	}
	items := c.GetRoot().GetCard().GetChildren()[0].GetPagedList().GetItems()
	var got []string
	for _, it := range items {
		got = append(got, it.GetPost().GetHandle()+"|"+it.GetPost().GetText())
	}
	want := []string{
		"@spydxdte|Статья в X",
		"@technews|Anthropic is said to weigh an IPO next year, sources say example.com/…",
		"@dev|Claude Code 刚刚发布了团队技能 🎉 チーム向けのスキル",
	}
	if len(got) != len(want) {
		t.Fatalf("posts %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("post %d: %q, want %q", i, got[i], want[i])
		}
	}
	goldenFixed(t, "tool_x_search", show(t, "ru", cards.Posts("turn-1:x", foundPosts)))
	if cards.Posts("k", foundPosts[:3]) != nil {
		t.Fatal("spam and bare links alone give no card")
	}
}

func TestWebResultsCardExample(t *testing.T) {
	rs := []providers.SearchResult{
		{URL: "https://www.reuters.com/a", Site: "reuters.com", Title: "Anthropic weighs an IPO as early as next year", Text: "The AI company has held early talks with banks…", Published: "2026-11-14T09:00:00Z"},
		{URL: "https://techcrunch.com/b", Site: "techcrunch.com", Title: "What an Anthropic IPO would mean for compute", Text: "Compute spending is the biggest line in the budget."},
		{URL: "https://ft.com/c", Site: "ft.com", Title: "AI labs and the public markets", Text: "Investors ask how the labs will pay for training runs."},
	}
	goldenFixed(t, "tool_web_search", show(t, "ru", cards.WebResults("turn-1:w", rs, nil)))
	// with the image proxy, each result's image is its link's thumbnail by the proxy's id, never its url
	rs[0].ImageURL = "https://img.example/a.jpg"
	refs := func(u string) string {
		if u == "https://img.example/a.jpg" {
			return "AbCdEfGhIjKlMnOpQrStUvWx"
		}
		return ""
	}
	c := show(t, "ru", cards.WebResultsWithImages("turn-1:w", rs, nil, refs))
	items := c.GetRoot().GetCard().GetChildren()[0].GetPagedList().GetItems()
	if got := items[0].GetLink().GetThumbnail().GetRef(); got != "img:AbCdEfGhIjKlMnOpQrStUvWx" {
		t.Fatalf("thumbnail %q", got)
	}
	if items[1].GetLink().GetThumbnail() != nil {
		t.Fatal("a result without an image got a thumbnail")
	}
	goldenFixed(t, "tool_web_search_images", c)
}

// goldenFixed is golden with the card's random action ids written as fixed ones
func goldenFixed(t *testing.T, name string, c *cardsv1.Card) {
	t.Helper()
	n := 0
	fixActions(c.ProtoReflect(), &n)
	golden(t, name, c)
}
