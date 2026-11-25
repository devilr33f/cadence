package cards

import (
	"testing"

	"google.golang.org/protobuf/proto"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

func areq(card string, rev uint32, action string, v *cardsv1.ActionValue) *cardsv1.ActionRequest {
	return &cardsv1.ActionRequest{CardId: card, Revision: rev, ActionId: action, Value: v}
}

func TestDecodeActionRequest(t *testing.T) {
	good := []*cardsv1.ActionRequest{
		areq("ha.light.living", 2, "a:7f3d", NumberValue(45)),
		areq("task.5b3e", 3, "a:k:answer", IDsValue("d12")),
		areq("task.5b3e", 3, "a:k:class", IDValue("economy")),
		areq("ha.light.living", 2, "a:7f3c", nil),
		{PageId: "page.q1.2", ActionId: "a:p:3"},
		// a page's next action from a screen before page ids
		areq("", 0, "a:p:3", nil),
	}
	for _, r := range good {
		b, _ := proto.Marshal(r)
		if _, err := DecodeActionRequest(b); err != nil {
			t.Fatalf("%v: %s", r, reason(err))
		}
	}
	bad := []struct {
		r    *cardsv1.ActionRequest
		code ErrorCode
	}{
		{areq("c", 1, "", nil), CodeRequired},
		{&cardsv1.ActionRequest{CardId: "c", PageId: "p", ActionId: "a:1"}, CodeValue},
		{&cardsv1.ActionRequest{PageId: "bad page", ActionId: "a:1"}, CodePattern},
		{areq("c", 1, "a 1", nil), CodePattern},
		{areq("c", 1, "a:1", IDValue("bad id")), CodePattern},
		{areq("c", 1, "a:1", IDsValue("a", "b", "c", "d", "e", "f", "g", "h", "i")), CodeLength},
		{areq("c", 1, "a:1", &cardsv1.ActionValue{}), CodeValue},
	}
	for _, c := range bad {
		b, _ := proto.Marshal(c.r)
		_, err := DecodeActionRequest(b)
		refusedWith(t, err, c.code)
	}
}

func TestCheckAction(t *testing.T) {
	light := example(t, "light_living_r2")
	clarify := example(t, "hermes_clarify")
	tiles := example(t, "search_results")
	post := example(t, "x_post")
	approval := example(t, "hermes_approval")
	cases := []struct {
		name string
		card *cardsv1.Card
		req  *cardsv1.ActionRequest
		code ErrorCode
		kind string
	}{
		{"slider in range", light, areq("ha.light.living", 2, "a:7f3d", NumberValue(45)), "", "slider"},
		{"slider at max", light, areq("ha.light.living", 2, "a:7f3d", NumberValue(100)), "", "slider"},
		{"slider over max", light, areq("ha.light.living", 2, "a:7f3d", NumberValue(101)), CodeValue, ""},
		{"slider without value", light, areq("ha.light.living", 2, "a:7f3d", nil), CodeValue, ""},
		{"slider with an id", light, areq("ha.light.living", 2, "a:7f3d", IDValue("x")), CodeValue, ""},
		{"toggle", light, areq("ha.light.living", 2, "a:7f3c", nil), "", "toggle"},
		{"toggle with a value", light, areq("ha.light.living", 2, "a:7f3c", NumberValue(1)), CodeValue, ""},
		{"stale revision", light, areq("ha.light.living", 1, "a:7f3c", nil), CodeStale, ""},
		{"another card", light, areq("ha.light.kitchen", 2, "a:7f3c", nil), CodeStale, ""},
		{"unknown action", light, areq("ha.light.living", 2, "a:nope", nil), CodeUnknownAction, ""},
		{"multiple choices", clarify, areq("task.5b3e", 3, "a:k:answer", IDsValue("d12", "d14")), "", "choices"},
		{"multiple over max", clarify, areq("task.5b3e", 3, "a:k:answer", IDsValue("d12", "d13", "d14", "d15")),
			CodeValue, ""},
		{"multiple empty", clarify, areq("task.5b3e", 3, "a:k:answer", IDsValue()), CodeValue, ""},
		{"multiple unknown id", clarify, areq("task.5b3e", 3, "a:k:answer", IDsValue("d99")), CodeValue, ""},
		{"multiple repeated", clarify, areq("task.5b3e", 3, "a:k:answer", IDsValue("d12", "d12")), CodeValue, ""},
		{"multiple given one id", clarify, areq("task.5b3e", 3, "a:k:answer", IDValue("d12")), CodeValue, ""},
		{"single choice", clarify, areq("task.5b3e", 3, "a:k:class", IDValue("business")), "", "choices"},
		{"single unknown", clarify, areq("task.5b3e", 3, "a:k:class", IDValue("first")), CodeValue, ""},
		{"single given a list", clarify, areq("task.5b3e", 3, "a:k:class", IDsValue("economy")), CodeValue, ""},
		{"tile secondary", tiles, areq("catalog.search.q1", 1, "a:s1m", nil), "", "media_tile"},
		{"list more row", tiles, areq("catalog.search.q1", 1, "a:s:more", nil), "", "paged_list"},
		{"post", post, areq("x.search.p1", 1, "a:x:92", nil), "", "post"},
		{"card action button", approval, areq("task.9f2a", 7, "a:k:deny", nil), "", "button"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := CheckAction(c.card, c.req)
			if c.code != "" {
				refusedWith(t, err, c.code)
				return
			}
			if err != nil {
				t.Fatalf("refused: %s", reason(err))
			}
			if kindOf(n) != c.kind {
				t.Fatalf("found %s, want %s", kindOf(n), c.kind)
			}
		})
	}
	if _, at, _ := FindAction(approval, "a:k:allow"); at != "/root/actions/1" {
		t.Fatalf("path %q", at)
	}
}
