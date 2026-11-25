package catalog

import (
	"testing"
)

// a title group's first variant is its default source, so it must not depend on which source answered a search
// first: sources are searched at once and answer in any order
func TestGroupVariantsDoNotFollowAnswerOrder(t *testing.T) {
	variant := func(source SourceID, id string) Item {
		return Item{Key: Key{Provider: "torrplayer", Kind: KindSeries, ID: id}, Source: source}
	}
	first := func(order ...Item) SourceID {
		s := New(Deps{})
		var g *group
		for _, it := range order {
			g, _ = s.joinGroup("house", "shikimori:1", it)
		}
		return s.variants(g)[0].Source
	}
	a, b := variant("aniliberty", "a-1"), variant("anime365", "b-1")
	if got, want := first(b, a), first(a, b); got != want || got != "aniliberty" {
		t.Fatalf("first variant %q answered b then a, %q answered a then b", got, want)
	}
}
