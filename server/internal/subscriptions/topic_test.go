package subscriptions

import (
	"slices"
	"testing"
)

func TestParseTopic(t *testing.T) {
	for _, c := range []struct {
		in, canon string
		path      []string
		wild      bool
	}{
		{"t3code:work:*", "t3code:work:*", nil, true},
		{"t3code:work:thread:abc", "t3code:work:thread:abc", []string{"thread", "abc"}, false},
		{"t3code:work:project:p1:*", "t3code:work:project:p1:*", []string{"project", "p1"}, true},
		// a separator or a star inside a segment is encoded, and decodes
		{"t3code:work:thread:a%3Ab", "t3code:work:thread:a%3Ab", []string{"thread", "a:b"}, false},
		{"t3code:work:thread:%2A", "t3code:work:thread:%2A", []string{"thread", "*"}, false},
		{"ha:home:entity:lock.front_door", "ha:home:entity:lock.front_door", []string{"entity", "lock.front_door"}, false},
		{"t3code:work:thread:a%20b", "t3code:work:thread:a%20b", []string{"thread", "a b"}, false},
	} {
		got, err := ParseTopic(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got.Module != c.in[:len(got.Module)] || !slices.Equal(got.Path, c.path) || got.Wild != c.wild || got.String() != c.canon {
			t.Fatalf("%s: %+v %q", c.in, got, got.String())
		}
	}
	for _, bad := range []string{
		"", "t3code", "t3code:work", "t3code:*", "t3code:*:thread", "t3code:work:*:x", "t3code:work::x",
		"T3:work:*", "t3code:-work:*", "t3code:work:thread:%zz", "t3code:wo%3Ark:*",
		"a:b:1:2:3:4:5:6:7:8:9:10:11:12:13:14:15",
	} {
		if _, err := ParseTopic(bad); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestTopicMatchesAndSpecificity(t *testing.T) {
	thread := NewTopic("t3code", "work", "thread", "a")
	for _, c := range []struct {
		sub   string
		match bool
	}{
		{"t3code:work:*", true},
		{"t3code:work:thread:*", true},
		{"t3code:work:thread:a", true},
		{"t3code:work:thread:a:*", true},
		{"t3code:work:thread:b", false},
		{"t3code:work:thread", false},
		{"t3code:home:*", false},
		{"hermes:work:*", false},
	} {
		if got := MustTopic(c.sub).Matches(thread); got != c.match {
			t.Fatalf("%s matches %v", c.sub, got)
		}
	}
	// a concrete topic never matches as a pattern does
	if MustTopic("t3code:work:thread:a").Matches(MustTopic("t3code:work:*")) {
		t.Fatal("wild matched")
	}
	order := []string{"t3code:work:*", "t3code:work:thread:*", "t3code:work:thread:a:*", "t3code:work:thread:a"}
	for i := 1; i < len(order); i++ {
		if MustTopic(order[i-1]).Specificity() >= MustTopic(order[i]).Specificity() {
			t.Fatalf("%s is not less specific than %s", order[i-1], order[i])
		}
	}
	// exact beats a subtree of the same literal prefix
	if MustTopic("t3code:work:thread:a").Specificity() <= MustTopic("t3code:work:thread:a:*").Specificity() {
		t.Fatal("exact not above its subtree")
	}
}
