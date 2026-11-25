package catalog

import (
	"errors"
	"testing"
)

func TestFailureIsUnplayableOnlyForDeadSources(t *testing.T) {
	for code, want := range map[FailureCode]bool{FailNoSeeders: true, FailRemoved: true, FailBlocked: true,
		FailTimeout: false, FailAuth: false} {
		err := error(&Failure{Provider: ProviderTorrplayer, Source: "nyaa", Code: code})
		if errors.Is(err, ErrUnplayable) != want {
			t.Errorf("%s: unplayable %v", code, !want)
		}
	}
	if got := (&Failure{Provider: ProviderTorrplayer, Source: "nyaa", Code: FailTimeout}).Error(); got !=
		"catalog: torrplayer/nyaa: timeout" {
		t.Fatalf("error %q", got)
	}
}

func TestGroupKey(t *testing.T) {
	cases := []struct {
		it   Item
		want string
	}{
		{Item{Show: &ShowInfo{IDs: ExternalIDs{Shikimori: "1", MAL: "2"}}}, "shikimori:1"},
		{Item{Show: &ShowInfo{IDs: ExternalIDs{MAL: "2"}}}, "mal:2"},
		{Item{Show: &ShowInfo{}}, ""},
		{Item{}, ""},
	}
	for _, c := range cases {
		got, ok := GroupKey(c.it)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%+v: %q %v", c.it.Show, got, ok)
		}
	}
}

func TestSelectionMerge(t *testing.T) {
	s := Selection{GroupQuality: "720"}.Merge(Selection{GroupQuality: "1080", GroupVoiceover: "anilibria"})
	if s[GroupQuality] != "720" || s[GroupVoiceover] != "anilibria" {
		t.Fatalf("merge %v", s)
	}
	if got := Selection(nil).Merge(nil); len(got) != 0 {
		t.Fatalf("empty merge %v", got)
	}
}

func TestKeyStringSeparatesFields(t *testing.T) {
	a := Key{Provider: ProviderTorrplayer, Source: "a", ID: "bc"}
	b := Key{Provider: ProviderTorrplayer, Source: "ab", ID: "c"}
	if a.String() == b.String() {
		t.Fatal("keys collide")
	}
}
