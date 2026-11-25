package i18n

import (
	"strings"
	"unicode/utf8"
)

// the pseudo-locale's letters for a-z and A-Z, one rune each (screen/i18n has the same table)
const (
	pseudoLower = "áƀçðéƒĝĥíĵķĺɱñóþǫŕšţúṽŵẋýž"
	pseudoUpper = "ÁƁÇÐÉƑĜĤÍĴĶĹṀÑÓÞǪŔŠŢÚṼŴẊÝŽ"
)

var pseudoLowerRunes, pseudoUpperRunes = []rune(pseudoLower), []rune(pseudoUpper)

// Pseudolocalize makes en's message into xx's: its letters accented, wrapped in [ ] and padded with · so it is
// about 40 % longer than the longest text it can render. Arguments, cases and # stay as they are.
func Pseudolocalize(m Message) Message {
	n := literalRunes(m)
	if n == 0 {
		return m
	}
	pad := (n*2 + 4) / 5 // ceil(n * 0.4)
	out := Message{nodes: []node{{kind: textNode, text: "["}}}
	out.nodes = append(out.nodes, accent(m).nodes...)
	out.nodes = append(out.nodes, node{kind: textNode, text: strings.Repeat("·", max(pad-2, 0)) + "]"})
	return out
}

func accent(m Message) Message {
	out := Message{nodes: make([]node, len(m.nodes))}
	for i, n := range m.nodes {
		if n.kind == textNode {
			var b strings.Builder
			for _, r := range n.text {
				switch {
				case r >= 'a' && r <= 'z':
					b.WriteRune(pseudoLowerRunes[r-'a'])
				case r >= 'A' && r <= 'Z':
					b.WriteRune(pseudoUpperRunes[r-'A'])
				default:
					b.WriteRune(r)
				}
			}
			n.text = b.String()
		}
		if len(n.cases) > 0 {
			cases := make([]branch, len(n.cases))
			for j, c := range n.cases {
				cases[j] = branch{key: c.key, msg: accent(c.msg)}
			}
			n.cases = cases
		}
		out.nodes[i] = n
	}
	return out
}

// literalRunes counts the text a message can render: its own literals and, for each plural or select, its longest case
func literalRunes(m Message) int {
	total := 0
	for _, n := range m.nodes {
		switch n.kind {
		case textNode:
			total += utf8.RuneCountInString(n.text)
		case pluralNode, selectNode:
			longest := 0
			for _, c := range n.cases {
				longest = max(longest, literalRunes(c.msg))
			}
			total += longest
		}
	}
	return total
}
