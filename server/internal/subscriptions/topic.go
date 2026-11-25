package subscriptions

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Wildcard, as a topic's last segment, matches its subtree: the segments
// before it and anything below them.
const Wildcard = "*"

const (
	maxTopic    = 512
	maxSegments = 16
)

// ErrTopic is a topic that does not parse.
var ErrTopic = errors.New("subscriptions: invalid topic")

var (
	moduleRe   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	instanceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Topic is <module>:<instance>:<path...>: module is the provider catalog's
// module, instance a provider instance's name and path the module's own;
// segments are percent-encoded, so a ':' or a '*' inside one is "%3A",
// "%2A". Wild is a topic ending in the wildcard segment, which Path leaves out.
type Topic struct {
	Module, Instance string
	Path             []string
	Wild             bool
}

// ParseTopic reads a topic: a module and an instance, then at least one path
// segment or the wildcard; the wildcard only as the last segment, no empty
// segment.
func ParseTopic(s string) (Topic, error) {
	if s == "" || len(s) > maxTopic {
		return Topic{}, ErrTopic
	}
	raw := strings.Split(s, ":")
	if len(raw) < 3 || len(raw) > maxSegments {
		return Topic{}, ErrTopic
	}
	t := Topic{}
	segs := make([]string, 0, len(raw))
	for i, r := range raw {
		if r == Wildcard {
			if i != len(raw)-1 || i < 2 {
				return Topic{}, ErrTopic
			}
			t.Wild = true
			break
		}
		seg, err := url.PathUnescape(r)
		if err != nil || seg == "" {
			return Topic{}, ErrTopic
		}
		segs = append(segs, seg)
	}
	t.Module, t.Instance, t.Path = segs[0], segs[1], segs[2:]
	if !moduleRe.MatchString(t.Module) || !instanceRe.MatchString(t.Instance) {
		return Topic{}, ErrTopic
	}
	if len(t.Path) == 0 {
		t.Path = nil
	}
	return t, nil
}

// MustTopic is ParseTopic for literals in code and tests.
func MustTopic(s string) Topic {
	t, err := ParseTopic(s)
	if err != nil {
		panic(err)
	}
	return t
}

// String is the topic in its canonical form, each segment encoded.
func (t Topic) String() string {
	parts := make([]string, 0, len(t.Path)+3)
	parts = append(parts, escape(t.Module), escape(t.Instance))
	for _, p := range t.Path {
		parts = append(parts, escape(p))
	}
	if t.Wild {
		parts = append(parts, Wildcard)
	}
	return strings.Join(parts, ":")
}

// escape percent-encodes a segment: what url.PathEscape does, and the
// topic's own separators.
func escape(s string) string {
	e := url.PathEscape(s)
	e = strings.ReplaceAll(e, ":", "%3A")
	return strings.ReplaceAll(e, "*", "%2A")
}

// Topic builds a concrete topic of the module's: a module's updates and
// entries name themselves this way.
func NewTopic(module, instance string, path ...string) Topic {
	return Topic{Module: module, Instance: instance, Path: path}
}

// Matches reports whether a subscription's topic t covers the concrete
// topic c: the same segments, or, for a wildcard topic, c begins with t's.
func (t Topic) Matches(c Topic) bool {
	if c.Wild || t.Module != c.Module || t.Instance != c.Instance {
		return false
	}
	if t.Wild {
		if len(c.Path) < len(t.Path) {
			return false
		}
	} else if len(c.Path) != len(t.Path) {
		return false
	}
	for i, p := range t.Path {
		if c.Path[i] != p {
			return false
		}
	}
	return true
}

// Specificity orders matching topics: more literal segments win, and an exact
// topic beats a wildcard one of as many.
func (t Topic) Specificity() int {
	s := 2 * (len(t.Path) + 2)
	if !t.Wild {
		s++
	}
	return s
}

// HasPrefix reports whether t's path begins with prefix.
func (t Topic) HasPrefix(prefix []string) bool {
	if len(prefix) > len(t.Path) {
		return false
	}
	for i, p := range prefix {
		if t.Path[i] != p {
			return false
		}
	}
	return true
}
