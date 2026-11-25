package catalog

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// SearchRequest is one search over the configured providers.
type SearchRequest struct {
	Query     string            `json:"query"`
	Kinds     []Kind            `json:"kinds,omitempty"`
	Sort      Sort              `json:"sort,omitempty"`
	Filters   map[string]string `json:"filters,omitempty"`
	Providers []ProviderID      `json:"providers,omitempty"`
	Sources   []SourceID        `json:"sources,omitempty"`
	// Cursor is an event's Next, with Providers and Sources naming its one
	// provider and source
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// searchTimeout bounds one provider's whole search
var searchTimeout = 20 * time.Second

type answer struct {
	p     Provider
	b     SearchBatch
	final bool
	err   error
}

// Search fans the query out to every configured provider that searches, at
// once, and emits a SearchEvent per provider and per source as each answers:
// the sources asked first, then each answer with its failure or its items,
// title groups merged by GroupKey as they arrive (a group already sent comes
// back in Updated), and a last event marked Done. A provider that does not
// answer in time fails its sources not yet heard with timeout. It returns an
// emit error (the caller gone) at once.
func (s *Service) Search(ctx context.Context, household string, req SearchRequest, emit func(SearchEvent) error) error {
	var ps []Provider
	for _, c := range s.d.Providers(ctx) {
		if c.Provider.Capabilities().Has(CapSearch) &&
			(len(req.Providers) == 0 || slices.Contains(req.Providers, c.Provider.ID())) {
			ps = append(ps, c.Provider)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pending := s.pending(ctx, ps, req.Sources)
	if err := emit(SearchEvent{Pending: pendingViews(pending)}); err != nil {
		return err
	}
	q := SearchQuery{Query: req.Query, Kinds: req.Kinds, Sort: req.Sort, Filters: req.Filters, Limit: req.Limit,
		Cursor: req.Cursor, Sources: req.Sources}
	answers := make(chan answer)
	var wg sync.WaitGroup
	for _, p := range ps {
		wg.Go(func() {
			pctx, pcancel := context.WithTimeout(ctx, searchTimeout)
			defer pcancel()
			err := p.Search(pctx, q, func(b SearchBatch) error {
				select {
				case answers <- answer{p: p, b: b}:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			if errors.Is(pctx.Err(), context.DeadlineExceeded) {
				err = &Failure{Provider: p.ID(), Code: FailTimeout}
			}
			select {
			case answers <- answer{p: p, final: true, err: err}:
			case <-ctx.Done():
			}
		})
	}
	go func() { wg.Wait(); close(answers) }()
	sent := map[Ref]bool{}
	for a := range answers {
		events := s.answered(ctx, household, a, pending, sent)
		for _, ev := range events {
			if err := emit(ev); err != nil {
				cancel()
				for range answers { //nolint:revive // drain: every provider's goroutine ends
				}
				return err
			}
		}
	}
	return emit(SearchEvent{Done: true})
}

type sourceKey struct {
	p ProviderID
	s SourceID
}

// pending are the sources a search asks, by provider, in order: a provider
// without sources is one with SourceID ""
func (s *Service) pending(ctx context.Context, ps []Provider, only []SourceID) map[sourceKey]SourceView {
	out := map[sourceKey]SourceView{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range ps {
		wg.Go(func() {
			sctx, cancel := context.WithTimeout(ctx, sourceWait)
			defer cancel()
			srcs, _ := p.Sources(sctx)
			mu.Lock()
			defer mu.Unlock()
			n := 0
			for _, src := range srcs {
				if src.Enabled && (len(only) == 0 || slices.Contains(only, src.ID)) {
					out[sourceKey{p.ID(), src.ID}] = SourceView{Provider: p.ID(), Source: src.ID, Name: src.Name,
						Enabled: true, Torrents: src.Torrents}
					n++
				}
			}
			if n == 0 {
				out[sourceKey{p.ID(), ""}] = SourceView{Provider: p.ID(), Name: string(p.ID()), Enabled: true}
			}
		})
	}
	wg.Wait()
	return out
}

func pendingViews(m map[sourceKey]SourceView) []SourceView {
	out := make([]SourceView, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b SourceView) int {
		if a.Provider != b.Provider {
			return compare(string(a.Provider), string(b.Provider))
		}
		return compare(string(a.Source), string(b.Source))
	})
	return out
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// answered turns one provider answer into events; a provider's end fails
// every source of it not heard yet
func (s *Service) answered(ctx context.Context, household string, a answer, pending map[sourceKey]SourceView, sent map[Ref]bool) []SearchEvent {
	id := a.p.ID()
	if a.final {
		var out []SearchEvent
		code := FailFailed
		if a.err != nil {
			code = codeOf(a.err)
		}
		left := []SourceView{}
		for k, v := range pending {
			if k.p == id {
				left = append(left, v)
				delete(pending, k)
			}
		}
		slices.SortFunc(left, func(x, y SourceView) int { return compare(string(x.Source), string(y.Source)) })
		for _, v := range left {
			if a.err == nil {
				// it ended without a word of this source: nothing found there
				out = append(out, SearchEvent{Provider: id, Source: v.Source})
				continue
			}
			s.d.Log.Warn("catalog_search_failed", "provider", string(id), "source", string(v.Source), "code", string(code))
			out = append(out, SearchEvent{Provider: id, Source: v.Source, Failure: &FailureView{Code: code}})
		}
		return out
	}
	b := a.b
	delete(pending, sourceKey{id, b.Source})
	delete(pending, sourceKey{id, ""})
	ev := SearchEvent{Provider: id, Source: b.Source, Next: b.Next, Unenforced: b.Unenforced}
	if b.Failure != nil {
		s.d.Log.Warn("catalog_search_failed", "provider", string(id), "source", string(b.Source), "code", string(b.Failure.Code))
		ev.Failure = &FailureView{Code: b.Failure.Code, HTTPStatus: b.Failure.HTTPStatus}
		return []SearchEvent{ev}
	}
	for _, it := range b.Items {
		o, err := s.out(ctx, household, a.p, it)
		if err != nil {
			continue
		}
		gk, ok := GroupKey(it)
		if !ok {
			ev.Items = append(ev.Items, o)
			continue
		}
		g, _ := s.joinGroup(household, gk, o)
		gi, err := s.out(ctx, household, a.p, s.groupItem(g))
		if err != nil {
			continue
		}
		if sent[gi.Ref] {
			ev.Updated = append(ev.Updated, gi)
			continue
		}
		sent[gi.Ref] = true
		ev.Items = append(ev.Items, gi)
	}
	if ev.Items == nil {
		ev.Items = []Item{}
	}
	return []SearchEvent{ev}
}
