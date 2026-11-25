package cards

import (
	"slices"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// FieldFeatures are the optional fields a card names in its features beside
// its primitives: the ones round 2 added to an existing primitive, which a
// screen built before them would skip silently.
var FieldFeatures = []string{
	"link.cite",
	"long_text.tail",
	"post.note",
	"post.quote",
	"post.reply_to",
	"post.reposted_by",
	"slider.timeline",
}

var nodeKinds = (&cardsv1.Node{}).ProtoReflect().Descriptor().Oneofs().ByName("kind")

// Primitives lists every primitive by its Node oneof name, in field order:
// card first (the root's alone), row and column as the design's one.
func Primitives() []string {
	fields := nodeKinds.Fields()
	out := make([]string, fields.Len())
	for i := range fields.Len() {
		out[i] = string(fields.Get(i).Name())
	}
	return out
}

// kindOf is a node's primitive name, "" for an empty node.
func kindOf(n *cardsv1.Node) string {
	if fd := n.ProtoReflect().WhichOneof(nodeKinds); fd != nil {
		return string(fd.Name())
	}
	return ""
}

// Features is what c uses: every primitive's name and every FieldFeatures
// entry that is set, sorted. A screen that lacks one draws the fallback text.
func Features(c *cardsv1.Card) []string {
	seen := map[string]bool{}
	var walk func(n *cardsv1.Node)
	walk = func(n *cardsv1.Node) {
		if n == nil {
			return
		}
		if k := kindOf(n); k != "" {
			seen[k] = true
		}
		for _, c := range children(n) {
			walk(c)
		}
		switch k := n.GetKind().(type) {
		case *cardsv1.Node_LongText:
			if k.LongText.GetTail() {
				seen["long_text.tail"] = true
			}
		case *cardsv1.Node_Link:
			if k.Link.GetCite() != 0 {
				seen["link.cite"] = true
			}
		case *cardsv1.Node_Post:
			postFeatures(k.Post, seen)
		case *cardsv1.Node_Slider:
			if k.Slider.GetTimeline() != nil {
				seen["slider.timeline"] = true
			}
		}
	}
	walk(c.GetRoot())
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

func postFeatures(p *cardsv1.Post, seen map[string]bool) {
	if p.GetNote() != nil {
		seen["post.note"] = true
	}
	if p.GetReplyTo() != "" {
		seen["post.reply_to"] = true
	}
	if p.GetRepostedBy() != "" {
		seen["post.reposted_by"] = true
	}
	if q := p.GetQuote(); q != nil {
		seen["post.quote"] = true
		postFeatures(q, seen)
	}
}

// children are the nodes directly below n: a container's children, a card's
// action buttons (as button nodes) and a paged list's items.
func children(n *cardsv1.Node) []*cardsv1.Node {
	switch k := n.GetKind().(type) {
	case *cardsv1.Node_Card:
		out := slices.Clone(k.Card.GetChildren())
		for _, b := range k.Card.GetActions() {
			out = append(out, N(b))
		}
		return out
	case *cardsv1.Node_Row:
		return k.Row.GetChildren()
	case *cardsv1.Node_Column:
		return k.Column.GetChildren()
	case *cardsv1.Node_Grid:
		return k.Grid.GetChildren()
	case *cardsv1.Node_PagedList:
		return k.PagedList.GetItems()
	}
	return nil
}
