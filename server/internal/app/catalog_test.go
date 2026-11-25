package app

import (
	"context"
	"net/http/httptest"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrplayer"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrplayer/torrplayertest"
)

// the agent's catalog walks the service's tree by refs: the item and its
// episodes, an episode's options, a play remembered, an action by the id the
// item read issued
func TestAgentCatalogOverTheService(t *testing.T) {
	fake := torrplayertest.New(torrplayertest.Frieren())
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, _ := torrplayer.NewClient(torrplayer.ClientConfig{BaseURL: srv.URL, Token: "fake-token"})
	ps := []catalog.Configured{{Instance: "anime", Provider: torrplayer.New(c)}}
	svc := catalog.New(catalog.Deps{Providers: func(context.Context) []catalog.Configured { return ps }})
	ctx := context.Background()
	var group catalog.Ref
	_ = svc.Search(ctx, catalog.Household, catalog.SearchRequest{Query: "фрирен"}, func(ev catalog.SearchEvent) error {
		for _, it := range ev.Items {
			if it.Kind == catalog.KindTitleGroup {
				group = it.Ref
			}
		}
		return nil
	})
	a := agentCatalog{s: svc}
	if it, ok := a.Cached(group); !ok || it.Kind != catalog.KindTitleGroup {
		t.Fatalf("cached %+v %v", it, ok)
	}
	d, err := a.Item(ctx, agent.CatalogItem{Ref: group})
	if err != nil || len(d.Children) != 3 || len(d.Variants) != 2 || d.Selected == nil || d.Continue == nil {
		t.Fatalf("item %+v %v", d, err)
	}
	ep := d.Children[1].Ref
	o, err := a.Options(ctx, ep, "", nil)
	if err != nil || len(o.Groups) == 0 || o.Selected[catalog.GroupVoiceover] == "" {
		t.Fatalf("options %+v %v", o, err)
	}
	p, err := a.Resolve(ctx, agent.CatalogResolve{Ref: group, Episode: "5",
		Selection: catalog.Selection{catalog.GroupVoiceover: "Crunchyroll"}, From: catalog.FromResume})
	if err != nil || p.Stream == nil || p.Stream.URL == "" || p.Episode != "5" || len(p.Stream.Skips) != 1 {
		t.Fatalf("resolve %+v %v", p, err)
	}
	var sub string
	for _, x := range d.Actions {
		if x.Kind == catalog.ActionSubscribe {
			sub = x.ID
		}
	}
	acts, err := a.Act(ctx, group, sub, true, "")
	if err != nil || len(acts) != 2 || !acts[1].Toggled {
		t.Fatalf("act %+v %v", acts, err)
	}
}

// seenDevice is a provider that keeps the device each resolve was for, as a relaying one registers its streams
type seenDevice struct {
	catalog.Provider
	devices []string
}

func (s *seenDevice) Resolve(ctx context.Context, r catalog.ResolveRequest) (catalog.Playable, error) {
	s.devices = append(s.devices, catalog.DeviceFrom(ctx))
	return s.Provider.Resolve(ctx, r)
}

// catalog_play's resolve names its device to the provider, so a relayed stream (YouTube's) counts against it
func TestAgentCatalogResolvesForTheDevice(t *testing.T) {
	fake := torrplayertest.New(torrplayertest.Frieren())
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, _ := torrplayer.NewClient(torrplayer.ClientConfig{BaseURL: srv.URL, Token: "fake-token"})
	seen := &seenDevice{Provider: torrplayer.New(c)}
	ps := []catalog.Configured{{Instance: "anime", Provider: seen}}
	svc := catalog.New(catalog.Deps{Providers: func(context.Context) []catalog.Configured { return ps }})
	ctx := context.Background()
	var group catalog.Ref
	_ = svc.Search(ctx, catalog.Household, catalog.SearchRequest{Query: "фрирен"}, func(ev catalog.SearchEvent) error {
		for _, it := range ev.Items {
			if it.Kind == catalog.KindTitleGroup {
				group = it.Ref
			}
		}
		return nil
	})
	a := agentCatalog{s: svc}
	if _, err := a.Resolve(ctx, agent.CatalogResolve{DeviceID: "tv-1", Ref: group, Episode: "5",
		Selection: catalog.Selection{catalog.GroupVoiceover: "Crunchyroll"}, From: catalog.FromResume}); err != nil {
		t.Fatal(err)
	}
	if len(seen.devices) != 1 || seen.devices[0] != "tv-1" {
		t.Fatalf("resolved for %q", seen.devices)
	}
}
