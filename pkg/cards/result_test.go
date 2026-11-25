package cards

import (
	"slices"
	"testing"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

func TestCheckPageAction(t *testing.T) {
	p := &cardsv1.Page{Version: Version, Id: "page.q1.2", Next: Act("a:p:3")}
	cases := []struct {
		name string
		req  *cardsv1.ActionRequest
		code ErrorCode
	}{
		{"by page id", &cardsv1.ActionRequest{PageId: "page.q1.2", ActionId: "a:p:3"}, ""},
		{"legacy, no ids", &cardsv1.ActionRequest{ActionId: "a:p:3"}, ""},
		{"another page", &cardsv1.ActionRequest{PageId: "page.q1.1", ActionId: "a:p:3"}, CodeStale},
		{"a card id", &cardsv1.ActionRequest{CardId: "c", ActionId: "a:p:3"}, CodeStale},
		{"another action", &cardsv1.ActionRequest{PageId: "page.q1.2", ActionId: "a:p:4"}, CodeUnknownAction},
		{"a value", &cardsv1.ActionRequest{PageId: "page.q1.2", ActionId: "a:p:3", Value: NumberValue(1)}, CodeValue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckPageAction(p, c.req)
			if c.code == "" {
				if err != nil {
					t.Fatal(reason(err))
				}
				return
			}
			refusedWith(t, err, c.code)
		})
	}
	if err := CheckPageAction(&cardsv1.Page{Version: Version, Id: "last"}, &cardsv1.ActionRequest{PageId: "last", ActionId: "a:p:3"}); err == nil {
		t.Fatal("the last page has no next action")
	}
}

func TestCheckActionNeedsACard(t *testing.T) {
	_, err := CheckAction(example(t, "light_living_r2"), &cardsv1.ActionRequest{PageId: "p", ActionId: "a:7f3c"})
	refusedWith(t, err, CodeRequired)
}

func TestActionResults(t *testing.T) {
	light := example(t, "light_living_r2")
	good := []*cardsv1.CardActionResult{
		CardResult(light),
		PageResult(&cardsv1.Page{Version: Version, Id: "page.1", Cards: []*cardsv1.Card{light}, Next: Act("a:p:2")}),
		RefusalResult("denied", "Нельзя с пульта", light),
		RefusalResult("expired", "", nil),
	}
	for _, r := range good {
		if err := PrepareActionResult(r); err != nil {
			t.Fatalf("%v: %s", r, reason(err))
		}
	}
	bad := []struct {
		name string
		r    *cardsv1.CardActionResult
		code ErrorCode
	}{
		{"empty", &cardsv1.CardActionResult{}, CodeRequired},
		{"no reason", RefusalResult("", "x", nil), CodeRequired},
		{"reason not a token", RefusalResult("Denied!", "", nil), CodePattern},
		{"page id pattern", PageResult(&cardsv1.Page{Version: Version, Id: "bad id"}), CodePattern},
		{"page version", PageResult(&cardsv1.Page{Version: 2}), CodeVersion},
		{"card features", CardResult(&cardsv1.Card{Version: Version, Id: "c", FallbackText: "x"}), CodeRequired},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) { refusedWith(t, ValidateActionResult(c.r), c.code) })
	}
}

func TestKnownAndMissing(t *testing.T) {
	known := Known()
	for _, f := range append(Primitives(), FieldFeatures...) {
		if !slices.Contains(known, f) {
			t.Fatalf("%s not known", f)
		}
	}
	post := example(t, "x_post")
	if m := Missing(post, known); len(m) != 0 {
		t.Fatalf("a screen drawing everything misses %v", m)
	}
	if m := Missing(post, nil); m != nil {
		t.Fatalf("a screen that sent no features misses %v", m)
	}
	m := Missing(post, []string{"card", "text"})
	if len(m) == 0 || slices.Contains(m, "card") {
		t.Fatalf("missing %v", m)
	}
}
