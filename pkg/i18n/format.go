package i18n

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Args are a message's arguments: strings for text and select, integers or floats for number and plural.
type Args map[string]any

// Format renders a parsed message. A missing or mistyped argument renders as {name} and is reported in the error;
// the text is still whole otherwise.
func (m Message) Format(l Lang, args Args) (string, error) {
	var b strings.Builder
	var errs []string
	m.format(&b, l, args, "", &errs)
	if len(errs) > 0 {
		return b.String(), fmt.Errorf("i18n: %s", strings.Join(errs, "; "))
	}
	return b.String(), nil
}

// Format parses and renders a message at once.
func Format(l Lang, msg string, args Args) (string, error) {
	m, err := Parse(msg)
	if err != nil {
		return msg, err
	}
	return m.Format(l, args)
}

func (m Message) format(b *strings.Builder, l Lang, args Args, pound string, errs *[]string) {
	for _, n := range m.nodes {
		switch n.kind {
		case textNode:
			b.WriteString(n.text)
		case poundNode:
			b.WriteString(pound)
		case argNode:
			switch v := args[n.name].(type) {
			case string:
				b.WriteString(v)
			case nil:
				*errs = append(*errs, "missing "+n.name)
				b.WriteString("{" + n.name + "}")
			default:
				if s, ok := numberText(l, v); ok {
					b.WriteString(s)
					continue
				}
				*errs = append(*errs, "bad "+n.name)
				b.WriteString("{" + n.name + "}")
			}
		case numberNode:
			s, ok := numberText(l, args[n.name])
			if !ok {
				*errs = append(*errs, "bad "+n.name)
				s = "{" + n.name + "}"
			}
			b.WriteString(s)
		case selectNode:
			v, ok := args[n.name].(string)
			if !ok {
				*errs = append(*errs, "bad "+n.name)
			}
			pick(n.cases, v).format(b, l, args, pound, errs)
		case pluralNode:
			plain, ok := plainNumber(args[n.name])
			if !ok {
				*errs = append(*errs, "bad "+n.name)
				plain = "0"
			}
			shown := localizeNumber(l, plain)
			chosen, found := Message{}, false
			for _, c := range n.cases {
				if c.key == "="+plain {
					chosen, found = c.msg, true
					break
				}
			}
			if !found {
				chosen = pick(n.cases, string(PluralOf(l, plain)))
			}
			chosen.format(b, l, args, shown, errs)
		}
	}
}

func pick(cases []branch, key string) Message {
	var other Message
	for _, c := range cases {
		if c.key == key {
			return c.msg
		}
		if c.key == "other" {
			other = c.msg
		}
	}
	return other
}

func numberText(l Lang, v any) (string, bool) {
	plain, ok := plainNumber(v)
	if !ok {
		return "", false
	}
	return localizeNumber(l, plain), true
}

// plainNumber is a number's digits with "." before a fraction, at most 3 fraction digits, trailing zeros dropped
func plainNumber(v any) (string, bool) {
	switch n := v.(type) {
	case int:
		return strconv.FormatInt(int64(n), 10), true
	case int8:
		return strconv.FormatInt(int64(n), 10), true
	case int16:
		return strconv.FormatInt(int64(n), 10), true
	case int32:
		return strconv.FormatInt(int64(n), 10), true
	case int64:
		return strconv.FormatInt(n, 10), true
	case uint:
		return strconv.FormatUint(uint64(n), 10), true
	case uint8:
		return strconv.FormatUint(uint64(n), 10), true
	case uint16:
		return strconv.FormatUint(uint64(n), 10), true
	case uint32:
		return strconv.FormatUint(uint64(n), 10), true
	case uint64:
		return strconv.FormatUint(n, 10), true
	case float32:
		return plainFloat(float64(n)), true
	case float64:
		return plainFloat(n), true
	}
	return "", false
}

func plainFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "0"
	}
	s := strconv.FormatFloat(f, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "-0" {
		return "0"
	}
	return s
}
