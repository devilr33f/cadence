package cards

import (
	"slices"
	"strconv"

	"google.golang.org/protobuf/proto"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// DecodeActionRequest decodes a screen's request and checks its fields (ids,
// value shape); CheckAction then checks it against the card.
func DecodeActionRequest(raw []byte) (*cardsv1.ActionRequest, error) {
	r := &cardsv1.ActionRequest{}
	if err := proto.Unmarshal(raw, r); err != nil {
		return nil, refuse(CodeDecode, "", "does not decode")
	}
	if err := checkFields(r.ProtoReflect(), ""); err != nil {
		return nil, err
	}
	if v := r.GetValue(); v != nil && v.GetKind() == nil {
		return nil, refuse(CodeValue, "/value", "a value with nothing set")
	}
	if r.GetCardId() != "" && r.GetPageId() != "" {
		return nil, refuse(CodeValue, "/pageId", "a request names a card or a page, not both")
	}
	return r, nil
}

// FindAction answers the node of c that carries action id, and its path: a
// toggle, button (a card action too), media tile (its own or a secondary
// action), paged list (its more row), link, post (a quoted one too), slider
// or choices.
func FindAction(c *cardsv1.Card, id string) (*cardsv1.Node, string, bool) {
	if id == "" {
		return nil, "", false
	}
	return findNode(c.GetRoot(), id, "/root")
}

func findNode(n *cardsv1.Node, id, at string) (*cardsv1.Node, string, bool) {
	if hasAction(n, id) {
		return n, at, true
	}
	kids := children(n)
	nChildren := len(kids)
	key := "children"
	switch k := n.GetKind().(type) {
	case *cardsv1.Node_Card:
		nChildren = len(k.Card.GetChildren())
	case *cardsv1.Node_PagedList:
		key = "items"
	}
	for i, c := range kids {
		path := at + "/" + key + "/" + strconv.Itoa(i)
		if i >= nChildren {
			path = at + "/actions/" + strconv.Itoa(i-nChildren)
		}
		if found, where, ok := findNode(c, id, path); ok {
			return found, where, true
		}
	}
	return nil, "", false
}

func hasAction(n *cardsv1.Node, id string) bool {
	is := func(a *cardsv1.Action) bool { return a != nil && a.GetId() == id }
	switch k := n.GetKind().(type) {
	case *cardsv1.Node_Toggle:
		return is(k.Toggle.GetAction())
	case *cardsv1.Node_Button:
		return is(k.Button.GetAction())
	case *cardsv1.Node_Link:
		return is(k.Link.GetAction())
	case *cardsv1.Node_Slider:
		return is(k.Slider.GetAction())
	case *cardsv1.Node_Choices:
		return is(k.Choices.GetAction())
	case *cardsv1.Node_PagedList:
		return is(k.PagedList.GetMore().GetAction())
	case *cardsv1.Node_MediaTile:
		return is(k.MediaTile.GetAction()) || slices.ContainsFunc(k.MediaTile.GetSecondary(),
			func(s *cardsv1.LabeledAction) bool { return is(s.GetAction()) })
	case *cardsv1.Node_Post:
		return is(k.Post.GetAction()) || is(k.Post.GetQuote().GetAction())
	}
	return false
}

// CheckAction refuses a request that does not fit the card the server issued:
// another card or revision, an action the card lacks, or a value its primitive
// does not take (a slider wants a number within its bounds, single choices
// one of its ids, multiple choices a set of them within min and max, and
// everything else no value). It answers the node the action belongs to. The
// server's own checks (who, when, single use, policy) come after.
func CheckAction(c *cardsv1.Card, r *cardsv1.ActionRequest) (*cardsv1.Node, error) {
	if r.GetCardId() == "" {
		return nil, refuse(CodeRequired, "/cardId", "missing")
	}
	if r.GetCardId() != c.GetId() || r.GetRevision() != c.GetRevision() {
		return nil, refuse(CodeStale, "/revision", "card %q revision %d, the request names %q revision %d",
			c.GetId(), c.GetRevision(), r.GetCardId(), r.GetRevision())
	}
	n, at, ok := FindAction(c, r.GetActionId())
	if !ok {
		return nil, refuse(CodeUnknownAction, "/actionId", "the card holds no action %q", r.GetActionId())
	}
	if err := checkActionValue(n, r.GetValue(), at); err != nil {
		return nil, err
	}
	return n, nil
}

func checkActionValue(n *cardsv1.Node, v *cardsv1.ActionValue, at string) *Error {
	switch k := n.GetKind().(type) {
	case *cardsv1.Node_Slider:
		num, ok := v.GetKind().(*cardsv1.ActionValue_Number)
		if !ok {
			return refuse(CodeValue, at, "a slider's action takes a number")
		}
		if err := finite(num.Number, at); err != nil {
			return refuse(CodeValue, at, "not a finite number")
		}
		if num.Number < k.Slider.GetMin() || num.Number > k.Slider.GetMax() {
			return refuse(CodeValue, at, "value %v is outside %v..%v", num.Number, k.Slider.GetMin(), k.Slider.GetMax())
		}
		return nil
	case *cardsv1.Node_Choices:
		c := k.Choices
		known := func(id string) bool {
			return slices.ContainsFunc(c.GetOptions(), func(o *cardsv1.Choice) bool { return o.GetId() == id })
		}
		if !c.GetMultiple() {
			id, ok := v.GetKind().(*cardsv1.ActionValue_Id)
			if !ok || !known(id.Id) {
				return refuse(CodeValue, at, "single choices take one option id")
			}
			return nil
		}
		ids, ok := v.GetKind().(*cardsv1.ActionValue_Ids)
		if !ok {
			return refuse(CodeValue, at, "multiple choices take a list of option ids")
		}
		lo, hi := choiceBounds(c)
		got := ids.Ids.GetIds()
		if len(got) < lo || len(got) > hi {
			return refuse(CodeValue, at, "%d options chosen, want %d..%d", len(got), lo, hi)
		}
		seen := map[string]bool{}
		for _, id := range got {
			if !known(id) || seen[id] {
				return refuse(CodeValue, at, "an unknown or repeated option id")
			}
			seen[id] = true
		}
		return nil
	}
	if v != nil {
		return refuse(CodeValue, at, "a %s action takes no value", kindOf(n))
	}
	return nil
}

// CheckPageAction refuses a page's next action that does not fit the page it
// was issued for: another page, the page's next action under another id, or
// a value. An empty page id is a screen's from before page ids, taken for one
// release.
func CheckPageAction(p *cardsv1.Page, r *cardsv1.ActionRequest) error {
	if r.GetCardId() != "" {
		return refuse(CodeStale, "/cardId", "a page's action names no card")
	}
	if r.GetPageId() != "" && r.GetPageId() != p.GetId() {
		return refuse(CodeStale, "/pageId", "page %q, the request names %q", p.GetId(), r.GetPageId())
	}
	if p.GetNext().GetId() == "" || r.GetActionId() != p.GetNext().GetId() {
		return refuse(CodeUnknownAction, "/actionId", "the page holds no action %q", r.GetActionId())
	}
	if r.GetValue() != nil {
		return refuse(CodeValue, "/value", "a page's next action takes no value")
	}
	return nil
}
