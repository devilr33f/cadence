package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const (
	tag = "mcp"

	codeSettings = "settings_unreadable"
)

func badInput(path string) error { return api.Error(http.StatusUnprocessableEntity, "invalid", path) }

func notFound() error { return api.Error(http.StatusNotFound, "not_found") }

// internal logs the operation and a code, never the error.
func (a *Admin) internal(op, code string) error {
	a.d.Log.Error("mcp_admin_failed", "op", op, "code", code)
	return api.Error(http.StatusInternalServerError, "internal")
}

// failed answers an error of the catalog or its store.
func (a *Admin) failed(op string, err error) error {
	switch {
	case errors.Is(err, ErrToolNotFound):
		return notFound()
	case errors.Is(err, ErrStaleAck):
		return api.Error(http.StatusConflict, "hash_mismatch")
	case errors.Is(err, ErrBadPatch):
		return badInput("")
	}
	return a.internal(op, CodeStore)
}

// param is a path parameter unescaped; false when it does not unescape. It
// is a copy: the catalog keeps a remote name as a map key.
func param(raw string) (string, bool) {
	v, err := url.PathUnescape(raw)
	return strings.Clone(v), err == nil
}

// strayParam is the first query parameter, as no route takes one.
func strayParam(q url.Values) string {
	if len(q) > 0 {
		return slices.Sorted(maps.Keys(q))[0]
	}
	return ""
}

// instance is the route's {name} when the mcp setting lists it, else the
// 422, 404 or 500 to answer.
func (a *Admin) instance(ctx context.Context, q url.Values, raw, op string) (string, error) {
	if s := strayParam(q); s != "" {
		return "", badInput(s)
	}
	names, err := a.d.Listed(ctx)
	if err != nil {
		return "", a.internal(op, codeSettings)
	}
	name, ok := param(raw)
	if !ok || !slices.Contains(names, name) {
		return "", notFound()
	}
	return name, nil
}

// fields reads a strict JSON object of the keys allowed; the path names the
// first other key, or is empty for a body that is no single object.
func fields(body []byte, allowed ...string) (map[string]json.RawMessage, string, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var m map[string]json.RawMessage
	if dec.Decode(&m) != nil || m == nil {
		return nil, "", false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, "", false
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if !slices.Contains(allowed, k) {
			return nil, k, false
		}
	}
	return m, "", true
}

// value decodes a field that must not be null.
func value[T any](raw json.RawMessage) (T, bool) {
	var v T
	if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &v) != nil {
		return v, false
	}
	return v, true
}

// patchBody is a PATCH body as read: acked says it named a hash, maybe an
// empty one.
type patchBody struct {
	Patch
	acked bool
}

// patchOf reads a PATCH body; on a refusal the path names the field, or is
// empty for a body that is no single object.
func patchOf(body []byte) (patchBody, string, bool) {
	m, path, ok := fields(body, "acknowledge", "anyTurn", "effects", "enabledDefault", "trust")
	if !ok {
		return patchBody{}, path, false
	}
	text := func(k string, allowed ...string) (*string, bool) {
		raw, given := m[k]
		if !given {
			return nil, true
		}
		s, ok := value[string](raw)
		return &s, ok && (len(allowed) == 0 || slices.Contains(allowed, s))
	}
	flag := func(k string) (*bool, bool) {
		raw, given := m[k]
		if !given {
			return nil, true
		}
		v, ok := value[bool](raw)
		return &v, ok
	}
	var b patchBody
	// in key order, so the first bad field is the one named
	ack, ok := text("acknowledge")
	if !ok {
		return b, "acknowledge", false
	}
	if b.AnyTurn, ok = flag("anyTurn"); !ok {
		return b, "anyTurn", false
	}
	if b.Effects, ok = text("effects", EffectsRead, EffectsWrite); !ok {
		return b, "effects", false
	}
	if b.EnabledDefault, ok = flag("enabledDefault"); !ok {
		return b, "enabledDefault", false
	}
	if b.Trust, ok = text("trust", TrustTrusted, TrustUntrusted); !ok {
		return b, "trust", false
	}
	if ack != nil {
		b.Ack, b.acked = *ack, true
	}
	return b, "", true
}

type (
	queryIn struct{ api.RawQuery }
	nameIn  struct {
		Name string `path:"name"`
		api.RawQuery
	}
	toolIn struct {
		Name   string `path:"name"`
		Remote string `path:"remote"`
		api.RawQuery
		api.RawJSON
	}
	ackIn struct {
		Name string `path:"name"`
		api.RawQuery
		api.RawJSON
	}

	serversOut  struct{ Body []serverView }
	toolsOut    struct{ Body []toolView }
	toolOut     struct{ Body toolView }
	acceptedOut struct {
		Body struct {
			Accepted bool `json:"accepted"`
		}
	}
)

func (a *Admin) servers(ctx context.Context, in *queryIn) (*serversOut, error) {
	if q := strayParam(in.Values()); q != "" {
		return nil, badInput(q)
	}
	names, err := a.d.Listed(ctx)
	if err != nil {
		return nil, a.internal("servers", codeSettings)
	}
	refused := a.refused()
	out := make([]serverView, 0, len(names))
	for _, name := range names {
		v, err := a.server(ctx, name, refused)
		if err != nil {
			return nil, a.internal("servers", CodeStore)
		}
		out = append(out, v)
	}
	return &serversOut{Body: out}, nil
}

// refresh asks for a re-list and answers at once: GET shows the outcome.
func (a *Admin) refresh(ctx context.Context, in *nameIn) (*acceptedOut, error) {
	name, err := a.instance(ctx, in.Values(), in.Name, "refresh")
	if err != nil {
		return nil, err
	}
	a.d.Catalog.Refresh(name)
	out := &acceptedOut{}
	out.Body.Accepted = true
	return out, nil
}

func (a *Admin) tools(ctx context.Context, in *nameIn) (*toolsOut, error) {
	name, err := a.instance(ctx, in.Values(), in.Name, "tools")
	if err != nil {
		return nil, err
	}
	return &toolsOut{Body: a.toolViews(name, a.refused())}, nil
}

// patch changes a tool's overrides: an acknowledge that is not the tool's
// latest hash applies nothing.
func (a *Admin) patch(ctx context.Context, in *toolIn) (*toolOut, error) {
	name, err := a.instance(ctx, in.Values(), in.Name, "patch")
	if err != nil {
		return nil, err
	}
	remote, ok := param(in.Remote)
	e, found := a.entry(name, remote)
	if !ok || !found {
		return nil, notFound()
	}
	b, path, ok := patchOf(in.Bytes())
	switch {
	case !ok:
		return nil, badInput(path)
	case b.acked && b.Ack == "":
		// an empty hash is no tool's
		return nil, api.Error(http.StatusConflict, "hash_mismatch")
	}
	o, err := a.d.Catalog.Override(ctx, name, remote, b.Patch)
	if err != nil {
		return nil, a.failed("patch", err)
	}
	if now, ok := a.entry(name, remote); ok {
		e = now
	} else {
		e.Override = o
	}
	return &toolOut{Body: view(e, a.refused())}, nil
}

// acknowledge acknowledges the tools named at their hashes, or every changed
// one, each or none, and answers the tool list.
func (a *Admin) acknowledge(ctx context.Context, in *ackIn) (*toolsOut, error) {
	name, err := a.instance(ctx, in.Values(), in.Name, "acknowledge")
	if err != nil {
		return nil, err
	}
	m, path, ok := fields(in.Bytes(), "hashes", "all")
	if !ok {
		return nil, badInput(path)
	}
	rawAll, all := m["all"]
	rawHashes, named := m["hashes"]
	var hashes map[string]string
	switch {
	case all == named:
		return nil, badInput("")
	case all:
		if v, ok := value[bool](rawAll); !ok || !v {
			return nil, badInput("all")
		}
	default:
		if hashes, ok = value[map[string]string](rawHashes); !ok {
			return nil, badInput("hashes")
		}
	}
	if err := a.d.Catalog.Acknowledge(ctx, name, hashes, all); err != nil {
		return nil, a.failed("acknowledge", err)
	}
	return &toolsOut{Body: a.toolViews(name, a.refused())}, nil
}

// RegisterAdmin registers the MCP operations on the admin api.
func RegisterAdmin(h huma.API, a *Admin) {
	defer api.Describe(h, docs)
	op := func(id, method, path string) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Tags: []string{tag}}
	}
	huma.Register(h, op("list-mcp-servers", http.MethodGet, "/mcp/servers"), a.servers)
	refresh := op("refresh-mcp-server", http.MethodPost, "/mcp/servers/{name}/refresh")
	refresh.DefaultStatus = http.StatusAccepted
	huma.Register(h, refresh, a.refresh)
	huma.Register(h, op("list-mcp-tools", http.MethodGet, "/mcp/servers/{name}/tools"), a.tools)
	huma.Register(h, op("patch-mcp-tool", http.MethodPatch, "/mcp/servers/{name}/tools/{remote}"), a.patch)
	huma.Register(h, op("acknowledge-mcp-tools", http.MethodPost, "/mcp/servers/{name}/acknowledge"), a.acknowledge)
}
