package ytweb

import (
	"maps"
	"slices"
	"strings"
)

// node is a decoded answer: YouTube's renderers move between releases, so the
// parsers read it by path and by renderer name, never through fixed structs.
type node = map[string]any

// at follows keys from v; nil where a key is missing or not an object
func at(v any, keys ...string) node {
	m, _ := v.(node)
	for _, k := range keys {
		if m == nil {
			return nil
		}
		m, _ = m[k].(node)
	}
	return m
}

func list(v any, keys ...string) []any {
	if len(keys) == 0 {
		l, _ := v.([]any)
		return l
	}
	l, _ := at(v, keys[:len(keys)-1]...)[keys[len(keys)-1]].([]any)
	return l
}

func str(v any, keys ...string) string {
	if len(keys) == 0 {
		s, _ := v.(string)
		return s
	}
	s, _ := at(v, keys[:len(keys)-1]...)[keys[len(keys)-1]].(string)
	return s
}

// text is a text object's string in any of its forms: simpleText, runs, or a
// view model's content
func text(v any) string {
	m, ok := v.(node)
	if !ok {
		return ""
	}
	if s, ok := m["simpleText"].(string); ok {
		return strings.TrimSpace(s)
	}
	if s, ok := m["content"].(string); ok {
		return strings.TrimSpace(s)
	}
	var b strings.Builder
	for _, r := range list(m, "runs") {
		b.WriteString(str(r, "text"))
	}
	return strings.TrimSpace(b.String())
}

// walk calls f with every key and value under v, objects' keys in sorted
// order (arrays keep theirs: an array's order is the ranking); f returns
// false to skip the value's insides
func walk(v any, f func(k string, x any) bool) {
	switch x := v.(type) {
	case node:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			if f(k, x[k]) {
				walk(x[k], f)
			}
		}
	case []any:
		for _, c := range x {
			walk(c, f)
		}
	}
}

// browseID is the first channel or playlist a text's links lead to: runs'
// navigationEndpoint, or a view model's commandRuns
func browseID(v any) string {
	for _, r := range list(v, "runs") {
		if id := str(r, "navigationEndpoint", "browseEndpoint", "browseId"); id != "" {
			return id
		}
	}
	for _, r := range list(v, "commandRuns") {
		if id := str(r, "onTap", "innertubeCommand", "browseEndpoint", "browseId"); id != "" {
			return id
		}
	}
	return ""
}

// clip bounds third-party text: the model and the screen need its start
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// anyOf is the first object that is there
func anyOf(ms ...node) node {
	for _, m := range ms {
		if m != nil {
			return m
		}
	}
	return nil
}
