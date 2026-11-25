package catalog

import (
	"context"
	"net/http"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/auth"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const tag = "catalog"

type noIn struct{}

type sourcesOut struct {
	Body struct {
		Providers []ProviderView `json:"providers" nullable:"false"`
	}
}

type searchIn struct {
	Q        string       `query:"q" maxLength:"256" doc:"The query; never logged."`
	Provider []ProviderID `query:"provider" maxItems:"8" doc:"Only these providers."`
	Source   []SourceID   `query:"source" maxItems:"16" doc:"Only these sources of a provider with sources."`
	Kind     []Kind       `query:"kind" maxItems:"16" doc:"Only these item kinds."`
	Sort     Sort         `query:"sort" enum:"relevance,popular" doc:"popular: the most viewed first, where a source can; one that cannot names sort in unenforced."`
	Uploaded string       `query:"uploaded" enum:"today,week,month,year" doc:"Only items uploaded this recently, where a source can."`
	Duration string       `query:"duration" enum:"short,medium,long" doc:"short is under 3 min, medium 3 to 20, long over 20, where a source can."`
	Cursor   string       `query:"cursor" maxLength:"256" doc:"An event's next, with provider and source naming it."`
	Limit    int          `query:"limit" minimum:"0" maximum:"100"`
}

type searchOut struct {
	Body struct {
		Events []SearchEvent `json:"events" nullable:"false"`
	}
}

type browseIn struct {
	Path   string `query:"path" maxLength:"512" doc:"provider/path, or empty for Home: every provider's root sections."`
	Cursor string `query:"cursor" maxLength:"256"`
	Limit  int    `query:"limit" minimum:"0" maximum:"100"`
}

type pageOut struct{ Body PageView }

type itemIn struct {
	Ref     Ref      `path:"ref" maxLength:"64"`
	Cursor  string   `query:"cursor" maxLength:"256"`
	Group   string   `query:"group" maxLength:"64" doc:"A child group's id (a season, an episode range)."`
	Choices []string `query:"choice,explode" maxItems:"8" doc:"A choice for this read, as group=id; repeat for several groups."`
	Tab     string   `query:"tab" maxLength:"32" doc:"One of the item's tabs (a channel's videos, shorts, streams, playlists)."`
	Sort    Sort     `query:"sort" enum:"latest,popular,oldest,updated,views" doc:"One of the item's sorts; an item without it answers its own order, named in sort."`
	Q       string   `query:"q" maxLength:"120" doc:"Search inside the item (a channel), or keep the children whose titles hold it; never logged."`
}

type detailOut struct{ Body DetailView }

type optionsIn struct {
	Ref     Ref      `path:"ref" maxLength:"64"`
	Episode string   `query:"episode" maxLength:"64"`
	Choices []string `query:"choice,explode" maxItems:"8" doc:"A choice already made, as group=id; repeat for several groups."`
}

type optionsOut struct{ Body OptionsView }

type alternativesIn struct {
	Ref Ref `path:"ref" maxLength:"64"`
}

type alternativesOut struct{ Body AlternativesView }

// choiceSelection reads repeated group=id query values. Only choice groups the
// catalog defines can reach a provider; malformed or duplicate groups are 422.
func choiceSelection(parts []string) (Selection, error) {
	out := Selection{}
	for _, part := range parts {
		kind, id, ok := strings.Cut(part, "=")
		group := GroupKind(kind)
		switch group {
		case GroupSource, GroupVoiceover, GroupQuality, GroupTorrent, GroupAudioLanguage, GroupSubtitles, GroupFile:
		default:
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid")
		}
		if !ok || id == "" || len(id) > 64 || out[group] != "" {
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid")
		}
		out[group] = id
	}
	return out, nil
}

// ActBody is an item action by the id an item read issued.
type ActBody struct {
	Action string `json:"action" maxLength:"64"`
	On     bool   `json:"on,omitempty" doc:"A toggle's new state."`
	Value  string `json:"value,omitempty" maxLength:"64" doc:"A valued action's (my list) new value."`
}

type actIn struct {
	Ref  Ref `path:"ref" maxLength:"64"`
	Body ActBody
}

type actOut struct {
	Body struct {
		Actions []ActionView `json:"actions" nullable:"false"`
	}
}

type panelIn struct {
	Ref    Ref    `path:"ref" maxLength:"64"`
	Panel  string `path:"panel" maxLength:"64"`
	Cursor string `query:"cursor" maxLength:"256"`
}

type panelOut struct{ Body PanelData }

// refuse is a service error as a code; never upstream text
func refuse(err error) error {
	why, code := RefusalOf(err)
	status := http.StatusInternalServerError
	switch why {
	case RefusalUnknownRef, RefusalUnknownAct, RefusalNotFound:
		status = http.StatusNotFound
	case RefusalUnsupported:
		status = http.StatusUnprocessableEntity
	case RefusalRefused, RefusalSignIn:
		status = http.StatusConflict
	case RefusalUnavailable:
		status = http.StatusBadGateway
	case RefusalCanceled:
		status = 499
	}
	if code != "" {
		return api.ErrorWithDetail(status, string(why), string(code))
	}
	return api.Error(status, string(why))
}

// docs names each operation's scope: the reads an ha token reaches too (the
// Home Assistant media browser), the rest stay admin's
var docs = map[string]api.Doc{
	"list-catalog-sources":     {Summary: "List the catalog's providers and their sources", Scope: auth.ScopeHA},
	"search-catalog":           {Summary: "Search every catalog provider", Scope: auth.ScopeHA},
	"browse-catalog":           {Summary: "Browse Home or a provider's feed or tree", Scope: auth.ScopeHA},
	"get-catalog-item":         {Summary: "Read a catalog item", Scope: auth.ScopeHA},
	"get-catalog-options":      {Summary: "Read an item's choice groups for an episode", Scope: auth.ScopeHA},
	"get-catalog-alternatives": {Summary: "List the providers and sources a title is available on", Scope: auth.ScopeHA},
	"act-catalog-item":         {Summary: "Run an item action"},
	"get-catalog-panel":        {Summary: "Read a page of an item's panel"},
}

// RegisterAdmin serves the catalog to the dashboard and an ha token's readers
// (the Home Assistant media browser) under /catalog: the configured providers
// and their sources, a search (every provider's events at once), browse, an
// item, its options, its actions and its panels. A play is the TV's alone
// (catalog.resolve): stream urls never reach the admin API. Refs are the
// household's, as a screen's.
func RegisterAdmin(a huma.API, s *Service) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "list-catalog-sources", Method: http.MethodGet, Path: "/catalog/sources", Tags: []string{tag},
		Description: "The providers the catalogSources setting names, in its order, each with its capabilities and its sources (torrplayer's AniLiberty, anime365, …), or the failure code its sources could not be read with.",
	}, func(ctx context.Context, _ *noIn) (*sourcesOut, error) {
		out := &sourcesOut{}
		out.Body.Providers = s.Providers(ctx)
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "search-catalog", Method: http.MethodGet, Path: "/catalog/search", Tags: []string{tag},
		Description: "The events a screen's streamed search gets, all at once: the sources asked, then each provider's and " +
			"source's items or failure (title groups merged by Shikimori, then MAL id; a group changed later comes " +
			"back in updated), then done. Filters a source ignored are named in unenforced.",
	}, func(ctx context.Context, in *searchIn) (*searchOut, error) {
		out := &searchOut{}
		out.Body.Events = []SearchEvent{}
		err := s.Search(ctx, Household, SearchRequest{Query: in.Q, Providers: in.Provider, Sources: in.Source,
			Kinds: in.Kind, Sort: in.Sort, Filters: searchFilters(in.Uploaded, in.Duration), Cursor: in.Cursor,
			Limit: in.Limit}, func(ev SearchEvent) error {
			out.Body.Events = append(out.Body.Events, ev)
			return nil
		})
		if err != nil {
			return nil, refuse(err)
		}
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "browse-catalog", Method: http.MethodGet, Path: "/catalog/browse", Tags: []string{tag},
		Description: "An empty path is Home (every provider's root sections); provider/path one of its feeds (continue, my_list, subscriptions, catalogue, library) or tree nodes (tree/…).",
	}, func(ctx context.Context, in *browseIn) (*pageOut, error) {
		pg, err := s.Browse(ctx, Household, in.Path, in.Cursor, nil, in.Limit)
		if err != nil {
			return nil, refuse(err)
		}
		return &pageOut{Body: pg}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-catalog-item", Method: http.MethodGet, Path: "/catalog/items/{ref}", Tags: []string{tag},
		Description: "Its children (episodes, tracks, files; a page, or a child group), a title group's variants and the one its remembered choices pick, where continue starts, its panels and actions (action ids issued for this read). 404 unknown_ref for a ref this server did not issue or that expired.",
	}, func(ctx context.Context, in *itemIn) (*detailOut, error) {
		sel, err := choiceSelection(in.Choices)
		if err != nil {
			return nil, err
		}
		d, err := s.ItemIn(ctx, Household, in.Ref, ItemPage{Cursor: in.Cursor, Group: in.Group, Tab: in.Tab, Sort: in.Sort,
			Query: in.Q}, sel)
		if err != nil {
			return nil, refuse(err)
		}
		return &detailOut{Body: d}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-catalog-options", Method: http.MethodGet, Path: "/catalog/items/{ref}/options", Tags: []string{tag},
		Description: "Source (a title group's), voiceover, quality, torrent, file: each group's choices, those that cannot play with a reason, and what a play would pick now (remembered marks the groups the remembered choice decided).",
	}, func(ctx context.Context, in *optionsIn) (*optionsOut, error) {
		sel, err := choiceSelection(in.Choices)
		if err != nil {
			return nil, err
		}
		o, err := s.Options(ctx, Household, in.Ref, in.Episode, sel)
		if err != nil {
			return nil, refuse(err)
		}
		return &optionsOut{Body: o}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-catalog-alternatives", Method: http.MethodGet, Path: "/catalog/items/{ref}/alternatives", Tags: []string{tag},
		Description: "The same title on every provider and sub-source, matched by Shikimori or MyAnimeList id before names, with its episode counts, voiceovers and best quality.",
	}, func(ctx context.Context, in *alternativesIn) (*alternativesOut, error) {
		v, err := s.Alternatives(ctx, Household, in.Ref)
		if err != nil {
			return nil, refuse(err)
		}
		return &alternativesOut{Body: v}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "act-catalog-item", Method: http.MethodPost, Path: "/catalog/items/{ref}/act", Tags: []string{tag},
		Description: "An action id an item read issued (subscribe, my list, watched, like): its new state and every action of the item again, with new ids. 404 unknown_action for an id not issued for the ref, 409 refused when the provider refuses it.",
	}, func(ctx context.Context, in *actIn) (*actOut, error) {
		acts, err := s.Act(ctx, Household, ActCall{Ref: in.Ref, Action: in.Body.Action, On: in.Body.On, Value: in.Body.Value})
		if err != nil {
			return nil, refuse(err)
		}
		out := &actOut{}
		out.Body.Actions = acts
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-catalog-panel", Method: http.MethodGet, Path: "/catalog/items/{ref}/panels/{panel}", Tags: []string{tag},
		Description: "A description, comments, lyrics, chapters or related items as data (screens get the same as cadence cards); next pages it.",
	}, func(ctx context.Context, in *panelIn) (*panelOut, error) {
		p, err := s.Panel(ctx, Household, in.Ref, in.Panel, in.Cursor)
		if err != nil {
			return nil, refuse(err)
		}
		return &panelOut{Body: p}, nil
	})
}

// searchFilters are the well-known filters a search names, none when it names none
func searchFilters(uploaded, duration string) map[string]string {
	f := map[string]string{}
	if uploaded != "" {
		f[FilterUploaded] = uploaded
	}
	if duration != "" {
		f[FilterDuration] = duration
	}
	if len(f) == 0 {
		return nil
	}
	return f
}
