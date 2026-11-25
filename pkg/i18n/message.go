package i18n

import (
	"fmt"
	"strconv"
	"strings"
)

// ArgType is what an argument of a message takes.
type ArgType string

const (
	// String is a plain {name} or a select's argument
	String ArgType = "string"
	// Number is {name, number} or a plural's argument
	Number ArgType = "number"
)

// Message is a parsed message: an icu messageformat subset.
//
//	text      literal; '' is one apostrophe, and '{', '}' or (inside a plural) '#' quote until the next lone '
//	{name}                     the argument as text
//	{name, number}             the argument as a locale number
//	{name, plural, =0 {...} one {...} other {...}}   other is required; # is the number in the locale
//	{name, select, a {...} other {...}}              other is required
type Message struct {
	nodes []node
}

type nodeKind int

const (
	textNode nodeKind = iota
	argNode
	numberNode
	pluralNode
	selectNode
	poundNode
)

type node struct {
	kind  nodeKind
	text  string
	name  string
	cases []branch
}

type branch struct {
	// "=3", a plural category, or a select key
	key string
	msg Message
}

// ParseError says where a message is malformed.
type ParseError struct {
	Offset int
	Reason string
}

func (e *ParseError) Error() string { return fmt.Sprintf("i18n: offset %d: %s", e.Offset, e.Reason) }

// Parse parses a message.
func Parse(s string) (Message, error) {
	p := parser{s: s}
	m, err := p.message(false)
	if err != nil {
		return Message{}, err
	}
	if p.i < len(s) {
		return Message{}, p.fail("unbalanced '}'")
	}
	return m, nil
}

type parser struct {
	s string
	i int
}

func (p *parser) fail(reason string) error { return &ParseError{Offset: p.i, Reason: reason} }

// message reads up to an unmatched '}' (left unread) or the end
func (p *parser) message(inPlural bool) (Message, error) {
	var m Message
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			m.nodes = append(m.nodes, node{kind: textNode, text: text.String()})
			text.Reset()
		}
	}
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '\'':
			p.quoted(&text, inPlural)
		case c == '{':
			flush()
			n, err := p.argument()
			if err != nil {
				return Message{}, err
			}
			m.nodes = append(m.nodes, n)
		case c == '}':
			flush()
			return m, nil
		case c == '#' && inPlural:
			flush()
			m.nodes = append(m.nodes, node{kind: poundNode})
			p.i++
		default:
			text.WriteByte(c)
			p.i++
		}
	}
	flush()
	return m, nil
}

// quoted handles an apostrophe at p.i (icu's DOUBLE_OPTIONAL)
func (p *parser) quoted(text *strings.Builder, inPlural bool) {
	if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
		text.WriteByte('\'')
		p.i += 2
		return
	}
	if p.i+1 < len(p.s) && (p.s[p.i+1] == '{' || p.s[p.i+1] == '}' || (inPlural && p.s[p.i+1] == '#')) {
		p.i++
		for p.i < len(p.s) {
			if p.s[p.i] == '\'' {
				if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
					text.WriteByte('\'')
					p.i += 2
					continue
				}
				p.i++
				return
			}
			text.WriteByte(p.s[p.i])
			p.i++
		}
		return
	}
	text.WriteByte('\'')
	p.i++
}

func (p *parser) space() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *parser) word() string {
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '_' || c == '=' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			p.i++
			continue
		}
		break
	}
	return p.s[start:p.i]
}

func validName(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// argument reads {...} from its '{'
func (p *parser) argument() (node, error) {
	p.i++
	p.space()
	name := p.word()
	if !validName(name) {
		return node{}, p.fail("bad argument name")
	}
	p.space()
	if p.i >= len(p.s) {
		return node{}, p.fail("unclosed argument")
	}
	if p.s[p.i] == '}' {
		p.i++
		return node{kind: argNode, name: name}, nil
	}
	if p.s[p.i] != ',' {
		return node{}, p.fail("expected ',' or '}'")
	}
	p.i++
	p.space()
	typ := p.word()
	p.space()
	switch typ {
	case "number":
		if p.i >= len(p.s) || p.s[p.i] != '}' {
			return node{}, p.fail("number takes no style")
		}
		p.i++
		return node{kind: numberNode, name: name}, nil
	case "plural", "select":
		if p.i >= len(p.s) || p.s[p.i] != ',' {
			return node{}, p.fail("expected ',' after " + typ)
		}
		p.i++
		kind := pluralNode
		if typ == "select" {
			kind = selectNode
		}
		cases, err := p.branches(kind)
		if err != nil {
			return node{}, err
		}
		return node{kind: kind, name: name, cases: cases}, nil
	default:
		return node{}, p.fail("unknown argument type " + strconv.Quote(typ))
	}
}

func (p *parser) branches(kind nodeKind) ([]branch, error) {
	var cases []branch
	seen := map[string]bool{}
	for {
		p.space()
		if p.i >= len(p.s) {
			return nil, p.fail("unclosed " + kindName(kind))
		}
		if p.s[p.i] == '}' {
			p.i++
			break
		}
		key := p.word()
		if key == "" {
			return nil, p.fail("expected a case key")
		}
		if err := p.checkKey(kind, key); err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, p.fail("duplicate case " + key)
		}
		seen[key] = true
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != '{' {
			return nil, p.fail("expected '{' after " + key)
		}
		p.i++
		m, err := p.message(kind == pluralNode)
		if err != nil {
			return nil, err
		}
		if p.i >= len(p.s) {
			return nil, p.fail("unclosed case " + key)
		}
		p.i++
		cases = append(cases, branch{key: key, msg: m})
	}
	if !seen["other"] {
		return nil, p.fail(kindName(kind) + " needs other")
	}
	return cases, nil
}

func (p *parser) checkKey(kind nodeKind, key string) error {
	if kind == selectNode {
		if !validName(key) {
			return p.fail("bad select key " + strconv.Quote(key))
		}
		return nil
	}
	if strings.HasPrefix(key, "=") {
		if _, err := strconv.ParseUint(key[1:], 10, 63); err != nil {
			return p.fail("bad explicit case " + strconv.Quote(key))
		}
		return nil
	}
	switch PluralCategory(key) {
	case Zero, One, Two, Few, Many, Other:
		return nil
	}
	return p.fail("unknown plural category " + strconv.Quote(key))
}

func kindName(k nodeKind) string {
	if k == selectNode {
		return "select"
	}
	return "plural"
}

// Arg is one argument a message takes.
type Arg struct {
	Name string
	Type ArgType
}

// ArgList lists the arguments a message takes in the order they first appear, cases included. A name used both as
// text and as a number is an error.
func (m Message) ArgList() ([]Arg, error) {
	var out []Arg
	seen := map[string]ArgType{}
	if err := m.collect(seen, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Args lists the arguments a message takes, by name.
func (m Message) Args() (map[string]ArgType, error) {
	list, err := m.ArgList()
	if err != nil {
		return nil, err
	}
	out := make(map[string]ArgType, len(list))
	for _, a := range list {
		out[a.Name] = a.Type
	}
	return out, nil
}

func (m Message) collect(seen map[string]ArgType, out *[]Arg) error {
	for _, n := range m.nodes {
		var t ArgType
		switch n.kind {
		case argNode, selectNode:
			t = String
		case numberNode, pluralNode:
			t = Number
		default:
			continue
		}
		if prev, ok := seen[n.name]; !ok {
			seen[n.name] = t
			*out = append(*out, Arg{Name: n.name, Type: t})
		} else if prev != t {
			return fmt.Errorf("i18n: argument %s is both %s and %s", n.name, prev, t)
		}
		for _, b := range n.cases {
			if err := b.msg.collect(seen, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// PluralKeys lists the category keys of every plural in the message, explicit =N cases left out.
func (m Message) PluralKeys() [][]PluralCategory {
	var out [][]PluralCategory
	for _, n := range m.nodes {
		if n.kind == pluralNode {
			var ks []PluralCategory
			for _, b := range n.cases {
				if !strings.HasPrefix(b.key, "=") {
					ks = append(ks, PluralCategory(b.key))
				}
			}
			out = append(out, ks)
		}
		for _, b := range n.cases {
			out = append(out, b.msg.PluralKeys()...)
		}
	}
	return out
}
