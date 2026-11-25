package tools_test

import (
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/favorites"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestMusicFeedbackRatesCurrentOrAResult(t *testing.T) {
	f := newMusicFake()
	f.feedback = &feedbackFake{}
	f.current = providers.Track{Source: providers.SourceYandex, Instance: "ym", ID: "42", Station: "user:onyourwave"}
	ts := f.toolset()
	feedback := ts[2]
	if feedback.Effects != agent.Write || !feedback.TaintExempt || feedback.EventTurns {
		t.Fatalf("feedback policy %+v", feedback)
	}
	out, terr := call(t, feedback, "d1", `{"id":"current","feedback":"like"}`)
	if terr != nil || !strings.Contains(string(out), `"favorite":true`) || !strings.Contains(string(out), `"providerSync":"updated"`) {
		t.Fatalf("current rating %s, %+v", out, terr)
	}
	search := ts[0]
	if _, terr := call(t, search, "d1", `{"query":"numb"}`); terr != nil {
		t.Fatal(terr)
	}
	if _, terr := call(t, feedback, "d1", `{"id":"r2","feedback":"dislike"}`); terr != nil {
		t.Fatal(terr)
	}
	if strings.Join(f.feedback.calls, "|") != "like yandex:42|dislike spotify:2nLt" {
		t.Fatalf("rating calls %v", f.feedback.calls)
	}
	for _, args := range []string{`{"id":"r9","feedback":"like"}`, `{"id":"spotify:2nLt","feedback":"like"}`} {
		if _, terr := call(t, feedback, "d1", args); terr == nil {
			t.Fatalf("accepted unbounded rating %s", args)
		}
	}
	f.feedback.err = providers.ErrSignInRequired
	if _, terr := call(t, feedback, "d1", `{"id":"current","feedback":"like"}`); terr == nil || terr.Code != "sign_in_required" {
		t.Fatalf("provider refusal %+v", terr)
	}
}

func TestMusicFavoritesIsAPlayableCadenceCollection(t *testing.T) {
	f := newMusicFake()
	f.feedback = &feedbackFake{entries: []favorites.Entry{
		{Track: providers.Track{Source: providers.SourceYandex, Instance: "ym", ID: "42", Title: "One"}},
		{Track: providers.Track{Source: providers.SourceYouTube, ID: "5qZQEq_C3vc", VideoID: "5qZQEq_C3vc", Title: "Two"}},
	}}
	ts := f.toolset()
	list, play := ts[3], ts[1]
	if list.Effects != agent.Read || list.Output != agent.Untrusted {
		t.Fatalf("favorites policy %+v", list)
	}
	out, terr := call(t, list, "d1", `{}`)
	if terr != nil || !strings.Contains(string(out), `"kind":"favorites"`) || !strings.Contains(string(out), `"provider":"Cadence"`) ||
		!strings.Contains(string(out), `"count":2`) || strings.Contains(string(out), `"42"`) {
		t.Fatalf("favorites result %s, %+v", out, terr)
	}
	if _, terr := call(t, play, "d1", `{"id":"r1"}`); terr != nil {
		t.Fatal(terr)
	}
	if strings.Join(f.plays, "|") != "d1 play 42+5qZQEq_C3vc from 0 radio=no" {
		t.Fatalf("favorite plays %v", f.plays)
	}
	if len(f.options) != 1 || f.options[0].Name != "Cadence favorites" || f.options[0].Radio {
		t.Fatalf("favorite options %+v", f.options)
	}
	if _, terr := call(t, ts[2], "d1", `{"id":"r1","feedback":"like"}`); terr == nil || terr.Code != "invalid_arguments" {
		t.Fatalf("rated a collection: %+v", terr)
	}
}
