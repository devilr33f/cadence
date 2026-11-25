// Package catalogscreen serves the catalog to screens: the catalog.* RPC
// methods on the screens router (search streamed as RpcEvent frames,
// resolve's stages too when subscribed) and an item's panels as Cadence
// Cards pages. Arguments and results are never logged (refs, queries,
// titles and urls are private, rule 8).
package catalogscreen

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// Deps are the methods' parts.
type Deps struct {
	Catalog *catalog.Service
	// Cards renders panel pages; nil answers panels unavailable
	Cards *cards.Service
	Log   *slog.Logger
}

// Register adds catalog.search, browse, item, options, resolve, act and
// panel to r.
func Register(r *calls.Router, d Deps) {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	m := methods{d: d}
	r.Register(rpc.CatalogSearch, m.search)
	r.Register(rpc.CatalogBrowse, m.browse)
	r.Register(rpc.CatalogItem, m.item)
	r.Register(rpc.CatalogOptions, m.options)
	r.Register(rpc.CatalogResolve, m.resolve)
	r.Register(rpc.CatalogAct, m.act)
	r.Register(rpc.CatalogPanel, m.panel)
	r.Register(rpc.CatalogAlternatives, m.alternatives)
}

type methods struct{ d Deps }

// decode reads strict JSON args: unknown fields and trailing data are invalid
func decode(raw []byte, into any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(into) != nil {
		return false
	}
	var rest json.RawMessage
	return dec.Decode(&rest) == io.EOF
}

func invalid() calls.Reply { return calls.Fail(rpc.StatusInvalid, "invalid") }

// refuse answers a service error as a status and a code, logging the
// method and the code alone
func (m methods) refuse(c calls.Call, err error) calls.Reply {
	why, code := catalog.RefusalOf(err)
	status := rpc.StatusInternal
	switch why {
	case catalog.RefusalUnknownRef, catalog.RefusalUnknownAct:
		status = rpc.StatusExpired
	case catalog.RefusalNotFound:
		status = rpc.StatusNotFound
	case catalog.RefusalUnsupported:
		status = rpc.StatusInvalid
	case catalog.RefusalRefused, catalog.RefusalSignIn:
		status = rpc.StatusRefused
	case catalog.RefusalUnavailable, catalog.RefusalCanceled:
		status = rpc.StatusUnavailable
	}
	m.d.Log.Info("catalog_call_refused", "device_id", c.DeviceID, "rpc_id", c.ID, "method", c.Method,
		"code", string(why), "failure", string(code))
	// the screen shows what failed as it is: the refusal and failure codes,
	// which provider and source, the upstream's http status (ids, codes and a
	// number alone: never an upstream url or text)
	body := map[string]any{"code": string(why)}
	if code != "" {
		body["failure"] = string(code)
	}
	if f := catalog.FailureOf(err); f != nil {
		if f.Provider != "" {
			body["provider"] = string(f.Provider)
		}
		if f.Source != "" {
			body["source"] = string(f.Source)
		}
		if f.HTTPStatus > 0 {
			body["httpStatus"] = f.HTTPStatus
		}
	}
	return calls.Reply{Status: status, JSON: body}
}

type searchArgs struct {
	Query     string               `json:"query"`
	Kinds     []catalog.Kind       `json:"kinds,omitempty"`
	Sort      catalog.Sort         `json:"sort,omitempty"`
	Filters   map[string]string    `json:"filters,omitempty"`
	Providers []catalog.ProviderID `json:"providers,omitempty"`
	Sources   []catalog.SourceID   `json:"sources,omitempty"`
	Cursor    string               `json:"cursor,omitempty"`
	Limit     int                  `json:"limit,omitempty"`
}

const (
	followLimit = 45 * time.Second
	maxQuery    = 256
	maxLimit    = 100
)

// search answers {pending} and, subscribed, streams the rest as events;
// unsubscribed it answers {events} once every provider answered.
func (m methods) search(ctx context.Context, c calls.Call) calls.Reply {
	var a searchArgs
	if !decode(c.Args, &a) || len(a.Query) > maxQuery || a.Limit < 0 || a.Limit > maxLimit || len(a.Filters) > 16 {
		return invalid()
	}
	req := catalog.SearchRequest(a)
	if !c.Subscribe {
		var events []catalog.SearchEvent
		err := m.d.Catalog.Search(ctx, catalog.Household, req, func(ev catalog.SearchEvent) error {
			events = append(events, ev)
			return nil
		})
		if err != nil {
			return m.refuse(c, err)
		}
		return calls.OK(map[string]any{"events": events})
	}
	first := make(chan catalog.SearchEvent, 1)
	rest := make(chan catalog.SearchEvent)
	errc := make(chan error, 1)
	// the search outlives the answer, bounded: a follow that never runs (the
	// answer could not go out) still ends it
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), followLimit)
	go func() {
		n := 0
		errc <- m.d.Catalog.Search(sctx, catalog.Household, req, func(ev catalog.SearchEvent) error {
			n++
			if n == 1 {
				first <- ev
				return nil
			}
			select {
			case rest <- ev:
				return nil
			case <-sctx.Done():
				return sctx.Err()
			}
		})
		close(rest)
	}()
	var ev catalog.SearchEvent
	select {
	case ev = <-first:
	case err := <-errc:
		cancel()
		return m.refuse(c, err)
	case <-ctx.Done():
		cancel()
		return m.refuse(c, ctx.Err())
	}
	return calls.Reply{Status: rpc.StatusOK, JSON: ev, Follow: func(fctx context.Context, s *calls.Stream) {
		defer cancel()
		go func() { <-fctx.Done(); cancel() }()
		for ev := range rest {
			if s.Event(ev, nil) != nil {
				cancel()
			}
		}
	}}
}

type browseArgs struct {
	Path    string            `json:"path"`
	Cursor  string            `json:"cursor,omitempty"`
	Filters map[string]string `json:"filters,omitempty"`
	Limit   int               `json:"limit,omitempty"`
}

func (m methods) browse(ctx context.Context, c calls.Call) calls.Reply {
	var a browseArgs
	if !decode(c.Args, &a) || len(a.Path) > 512 || a.Limit < 0 || a.Limit > maxLimit {
		return invalid()
	}
	pg, err := m.d.Catalog.Browse(ctx, catalog.Household, a.Path, a.Cursor, a.Filters, a.Limit)
	if err != nil {
		return m.refuse(c, err)
	}
	return calls.OK(pg)
}

type itemArgs struct {
	Ref    catalog.Ref `json:"ref"`
	Cursor string      `json:"cursor,omitempty"`
	Group  string      `json:"group,omitempty"`
}

func (m methods) item(ctx context.Context, c calls.Call) calls.Reply {
	var a itemArgs
	if !decode(c.Args, &a) || a.Ref == "" {
		return invalid()
	}
	d, err := m.d.Catalog.Item(ctx, catalog.Household, a.Ref, a.Cursor, a.Group)
	if err != nil {
		return m.refuse(c, err)
	}
	return calls.OK(d)
}

type alternativesArgs struct {
	Ref catalog.Ref `json:"ref"`
}

// alternatives answers the same title on the other providers and sources, each with its voiceovers and quality
func (m methods) alternatives(ctx context.Context, c calls.Call) calls.Reply {
	var a alternativesArgs
	if !decode(c.Args, &a) || a.Ref == "" {
		return invalid()
	}
	v, err := m.d.Catalog.Alternatives(ctx, catalog.Household, a.Ref)
	if err != nil {
		return m.refuse(c, err)
	}
	return calls.OK(v)
}

type optionsArgs struct {
	Ref     catalog.Ref       `json:"ref"`
	Episode string            `json:"episode,omitempty"`
	Choices catalog.Selection `json:"choices,omitempty"`
}

func (m methods) options(ctx context.Context, c calls.Call) calls.Reply {
	var a optionsArgs
	if !decode(c.Args, &a) || a.Ref == "" {
		return invalid()
	}
	o, err := m.d.Catalog.Options(ctx, catalog.Household, a.Ref, a.Episode, a.Choices)
	if err != nil {
		return m.refuse(c, err)
	}
	return calls.OK(o)
}

type resolveArgs struct {
	Ref     catalog.Ref         `json:"ref"`
	Episode string              `json:"episode,omitempty"`
	Choices catalog.Selection   `json:"choices,omitempty"`
	From    catalog.ResolveFrom `json:"from,omitempty"`
}

// resolve answers the playable; subscribed, it answers {resolving: true} at
// once and streams {stage} events, then {playable} or {refused}, so BACK
// (RpcCancel) can stop a torrent's wait.
func (m methods) resolve(ctx context.Context, c calls.Call) calls.Reply {
	var a resolveArgs
	if !decode(c.Args, &a) || a.Ref == "" || (a.From != "" && a.From != catalog.FromStart && a.From != catalog.FromResume) {
		return invalid()
	}
	req := catalog.PlayRequest{Ref: a.Ref, Episode: a.Episode, Choices: a.Choices, From: a.From, DeviceID: c.DeviceID}
	if !c.Subscribe {
		pl, err := m.d.Catalog.Resolve(ctx, catalog.Household, req)
		if err != nil {
			return m.refuse(c, err)
		}
		return calls.OK(pl)
	}
	return calls.Reply{Status: rpc.StatusOK, JSON: map[string]bool{"resolving": true},
		Follow: func(fctx context.Context, s *calls.Stream) {
			req.OnStage = func(st catalog.Stage) { _ = s.Event(map[string]catalog.Stage{"stage": st}, nil) }
			pl, err := m.d.Catalog.Resolve(fctx, catalog.Household, req)
			if err != nil {
				rep := m.refuse(c, err)
				_ = s.Event(map[string]any{"status": rep.Status, "refused": rep.JSON}, nil)
				return
			}
			_ = s.Event(map[string]any{"playable": pl}, nil)
		}}
}

type actArgs struct {
	Ref    catalog.Ref `json:"ref"`
	Action string      `json:"action"`
	On     bool        `json:"on,omitempty"`
	Value  string      `json:"value,omitempty"`
}

func (m methods) act(ctx context.Context, c calls.Call) calls.Reply {
	var a actArgs
	if !decode(c.Args, &a) || a.Ref == "" || a.Action == "" || len(a.Value) > 64 {
		return invalid()
	}
	acts, err := m.d.Catalog.Act(ctx, catalog.Household, catalog.ActCall(a))
	if err != nil {
		return m.refuse(c, err)
	}
	m.d.Log.Info("catalog_act", "device_id", c.DeviceID, "rpc_id", c.ID)
	return calls.OK(map[string]any{"actions": acts})
}

type panelArgs struct {
	Ref    catalog.Ref `json:"ref"`
	Panel  string      `json:"panel"`
	Cursor string      `json:"cursor,omitempty"`
}

// panel answers a page of cadence cards; its next action is a card action
// that loads the following page.
func (m methods) panel(ctx context.Context, c calls.Call) calls.Reply {
	var a panelArgs
	if !decode(c.Args, &a) || a.Ref == "" || a.Panel == "" || len(a.Panel) > 64 {
		return invalid()
	}
	return m.panelOf(ctx, c, a)
}

// SessionPanel serves media.panel {sessionId, panelId, cursor?} for the sessions refOf names the item of (the
// catalog sessions the server played): the item's panel page, as catalog.panel answers it.
func SessionPanel(d Deps, refOf func(deviceID, sessionID string) (catalog.Ref, bool)) calls.Method {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	m := methods{d: d}
	return func(ctx context.Context, c calls.Call) calls.Reply {
		var a struct {
			SessionID string `json:"sessionId"`
			PanelID   string `json:"panelId"`
			Cursor    string `json:"cursor,omitempty"`
		}
		if !decode(c.Args, &a) || a.SessionID == "" || a.PanelID == "" || len(a.PanelID) > 64 || len(a.Cursor) > 256 {
			return invalid()
		}
		ref, ok := refOf(c.DeviceID, a.SessionID)
		if !ok {
			return calls.Fail(rpc.StatusNotFound, "no_session")
		}
		return m.panelOf(ctx, c, panelArgs{Ref: ref, Panel: a.PanelID, Cursor: a.Cursor})
	}
}

func (m methods) panelOf(ctx context.Context, c calls.Call, a panelArgs) calls.Reply {
	if m.d.Cards == nil {
		return calls.Fail(rpc.StatusUnavailable, "unavailable")
	}
	p, err := Panel(ctx, m.d.Catalog, a.Ref, a.Panel, a.Cursor)
	if err != nil {
		return m.refuse(c, err)
	}
	page, err := m.d.Cards.Page(ctx, c.DeviceID, p)
	if err != nil {
		return calls.Fail(rpc.StatusInternal, "internal")
	}
	return calls.Reply{Status: rpc.StatusOK, Page: page}
}
