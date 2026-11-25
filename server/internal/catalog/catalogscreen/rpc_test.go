package catalogscreen_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogscreen"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

type sink struct {
	mu  sync.Mutex
	got []*cadencev2.Envelope
}

func (s *sink) send(_ string, env *cadencev2.Envelope) error {
	s.mu.Lock()
	s.got = append(s.got, env)
	s.mu.Unlock()
	return nil
}

// until waits for the call's last frame (its response, or its last event)
func (s *sink) until(t *testing.T, id string, last bool) []*cadencev2.Envelope {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		var mine []*cadencev2.Envelope
		done := false
		for _, e := range s.got {
			switch b := e.GetBody().(type) {
			case *cadencev2.Envelope_RpcResponse:
				if b.RpcResponse.GetId() == id {
					mine = append(mine, e)
					done = done || !last
				}
			case *cadencev2.Envelope_RpcEvent:
				if b.RpcEvent.GetId() == id {
					mine = append(mine, e)
					done = done || b.RpcEvent.GetLast()
				}
			}
		}
		s.mu.Unlock()
		if done {
			return mine
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("call %s did not end", id)
	return nil
}

type rig struct {
	r *calls.Router
	s *sink
	n int

	mu         sync.Mutex
	sessionRef catalog.Ref
}

func newRig(t *testing.T) *rig {
	f := catalogtest.Torrplayer()
	k := f.Add(catalogtest.Entry{Item: catalog.Item{Key: catalogtest.FrierenAnime365, Title: "Фрирен", Show: &catalog.ShowInfo{
		IDs: catalog.ExternalIDs{Shikimori: "52991"}}}})
	_ = k
	f.Caps = append(f.Caps, catalog.CapPanel)
	f.Add(catalogtest.Entry{
		Item:   catalog.Item{Key: catalogtest.FrierenAniLiberty, Title: "Провожающая в последний путь Фрирен", Show: &catalog.ShowInfo{IDs: catalog.ExternalIDs{Shikimori: "52991"}}},
		Panels: []catalog.Panel{{ID: "comments", Kind: catalog.PanelComments, Title: "Комментарии"}},
		Pages: map[string]catalog.PanelPage{
			"comments\x1f":   {Comments: []catalog.Comment{{Author: "Анна", Text: "Лучшее аниме сезона", Likes: 3}}, Next: "p2"},
			"comments\x1fp2": {Comments: []catalog.Comment{{Author: "Игорь", Text: "Согласен"}}},
		},
		Children: []catalog.Key{catalogtest.FrierenEpisode5},
	})
	svc := catalog.New(catalog.Deps{Providers: func(context.Context) []catalog.Configured {
		return []catalog.Configured{{Instance: "anime", Provider: f}}
	}})
	s := &sink{}
	r := calls.New(s.send, slog.New(slog.DiscardHandler))
	t.Cleanup(r.Close)
	d := catalogscreen.Deps{Catalog: svc, Cards: cards.New(cards.Deps{})}
	catalogscreen.Register(r, d)
	g := &rig{r: r, s: s}
	r.Register(rpc.MediaPanel, catalogscreen.SessionPanel(d, func(dev, session string) (catalog.Ref, bool) {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.sessionRef, dev == "tv" && session == "video-1" && g.sessionRef != ""
	}))
	return g
}

func (g *rig) call(t *testing.T, method string, args any, subscribe bool) []*cadencev2.Envelope {
	t.Helper()
	g.n++
	id := method + "-" + string(rune('a'+g.n))
	b, _ := json.Marshal(args)
	g.r.Handle("tv", &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcRequest{RpcRequest: &cadencev2.RpcRequest{
		Id: id, Method: method, Json: string(b), Subscribe: subscribe}}})
	return g.s.until(t, id, subscribe)
}

func answer(t *testing.T, env *cadencev2.Envelope, into any) string {
	t.Helper()
	r := env.GetRpcResponse()
	if into != nil {
		if err := json.Unmarshal([]byte(r.GetJson()), into); err != nil {
			t.Fatal(err)
		}
	}
	return r.GetStatus()
}

func TestSearchStreamsAndTheTreeWalks(t *testing.T) {
	g := newRig(t)
	frames := g.call(t, rpc.CatalogSearch, map[string]string{"query": "фрирен"}, true)
	var first catalog.SearchEvent
	if st := answer(t, frames[0], &first); st != rpc.StatusOK || len(first.Pending) != 3 {
		t.Fatalf("first %s %+v", st, first)
	}
	var group catalog.Ref
	failed := 0
	for _, f := range frames[1:] {
		ev := f.GetRpcEvent()
		var e catalog.SearchEvent
		_ = json.Unmarshal([]byte(ev.GetJson()), &e)
		if e.Failure != nil {
			failed++
		}
		for _, it := range e.Items {
			if it.Kind == catalog.KindTitleGroup {
				group = it.Ref
			}
		}
	}
	if group == "" || failed != 1 || !frames[len(frames)-1].GetRpcEvent().GetLast() {
		t.Fatalf("group %q failed %d", group, failed)
	}
	var d catalog.DetailView
	if st := answer(t, g.call(t, rpc.CatalogItem, map[string]any{"ref": group}, false)[0], &d); st != rpc.StatusOK || len(d.Variants) != 2 {
		t.Fatalf("item %s %+v", st, d)
	}
	var alts catalog.AlternativesView
	if st := answer(t, g.call(t, rpc.CatalogAlternatives, map[string]any{"ref": group}, false)[0], &alts); st != rpc.StatusOK ||
		len(alts.Items) != 2 || alts.Items[0].Match != "shikimori" {
		t.Fatalf("alternatives %s %+v", st, alts)
	}
	if st := answer(t, g.call(t, rpc.CatalogAlternatives, map[string]any{"ref": "c:nope"}, false)[0], nil); st != rpc.StatusExpired {
		t.Fatalf("alternatives of an unknown ref %s", st)
	}
	var o catalog.OptionsView
	if st := answer(t, g.call(t, rpc.CatalogOptions, map[string]any{"ref": d.Children[0].Ref}, false)[0], &o); st != rpc.StatusOK || len(o.Groups) != 2 {
		t.Fatalf("options %s %+v", st, o)
	}
	frames = g.call(t, rpc.CatalogResolve, map[string]any{"ref": d.Children[0].Ref, "choices": map[string]string{"voiceover": "subs"}}, true)
	last := ""
	for _, f := range frames[1:] {
		if ev := f.GetRpcEvent(); strings.Contains(ev.GetJson(), "playable") {
			last = ev.GetJson()
		}
	}
	if !strings.Contains(last, `"url":"http://torrplayer.lan/resolve/r1"`) || !strings.Contains(last, `"voiceover":"subs"`) {
		t.Fatalf("resolve events %q", last)
	}
	if st := answer(t, g.call(t, rpc.CatalogItem, map[string]any{"ref": "c:nope"}, false)[0], nil); st != rpc.StatusExpired {
		t.Fatalf("unknown ref %s", st)
	}
	if st := answer(t, g.call(t, rpc.CatalogItem, map[string]any{"ref": group, "bogus": 1}, false)[0], nil); st != rpc.StatusInvalid {
		t.Fatalf("bad args %s", st)
	}
}

func TestPanelIsACardsPage(t *testing.T) {
	g := newRig(t)
	frames := g.call(t, rpc.CatalogSearch, map[string]string{"query": "Провожающая"}, false)
	var all struct{ Events []catalog.SearchEvent }
	answer(t, frames[0], &all)
	var ref catalog.Ref
	for _, ev := range all.Events {
		for _, it := range ev.Items {
			ref = it.Ref
		}
	}
	resp := g.call(t, rpc.CatalogPanel, map[string]any{"ref": ref, "panel": "comments"}, false)[0].GetRpcResponse()
	page := resp.GetPage()
	if resp.GetStatus() != rpc.StatusOK || page == nil || len(page.GetCards()) != 1 || page.GetNext().GetId() == "" || page.GetId() == "" {
		t.Fatalf("page %s %v", resp.GetStatus(), page)
	}
	// media.panel reads the same page for the session that plays the item
	if resp := g.call(t, rpc.MediaPanel, map[string]any{"sessionId": "video-1", "panelId": "comments"}, false)[0].GetRpcResponse(); resp.GetStatus() != rpc.StatusNotFound {
		t.Fatalf("no session yet %v", resp)
	}
	g.mu.Lock()
	g.sessionRef = ref
	g.mu.Unlock()
	if resp := g.call(t, rpc.MediaPanel, map[string]any{"sessionId": "video-1", "panelId": "comments"}, false)[0].GetRpcResponse(); resp.GetStatus() != rpc.StatusOK || len(resp.GetPage().GetCards()) != 1 {
		t.Fatalf("media.panel %v", resp)
	}
	if page.GetCards()[0].GetRoot().GetCard().GetChildren()[0].GetPagedList().GetItems()[0].GetPost().GetText() != "Лучшее аниме сезона" {
		t.Fatal("comment not drawn")
	}
}

// a refusal says what failed as it is: the codes, which provider and source,
// the upstream's http status, and nothing an upstream said
func TestRefusalNamesTheFailure(t *testing.T) {
	f := catalogtest.Torrplayer()
	f.Caps = append(f.Caps, catalog.CapBrowse)
	f.BrowseErr = &catalog.Failure{Provider: catalog.ProviderTorrplayer, Source: "anime365", Code: catalog.FailFailed, HTTPStatus: 502}
	svc := catalog.New(catalog.Deps{Providers: func(context.Context) []catalog.Configured {
		return []catalog.Configured{{Instance: "anime", Provider: f}}
	}})
	s := &sink{}
	r := calls.New(s.send, slog.New(slog.DiscardHandler))
	t.Cleanup(r.Close)
	catalogscreen.Register(r, catalogscreen.Deps{Catalog: svc, Cards: cards.New(cards.Deps{})})
	g := &rig{r: r, s: s}
	var body map[string]any
	st := answer(t, g.call(t, rpc.CatalogBrowse, map[string]any{"path": "torrplayer"}, false)[0], &body)
	if st != rpc.StatusUnavailable {
		t.Fatalf("status %s", st)
	}
	want := map[string]any{"code": "unavailable", "failure": "failed", "provider": "torrplayer", "source": "anime365", "httpStatus": float64(502)}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("%s: %v, want %v (%v)", k, body[k], v, body)
		}
	}
	if len(body) != len(want) {
		t.Fatalf("extra fields %v", body)
	}
}
