package tracing

import (
	"context"
	"net/http"
	"regexp"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

// tag is the document's tag of the tracing operations
const tag = "tracing"

var docs = map[string]api.Doc{
	"list-traces":     {Summary: "List the retained traces, newest first"},
	"get-trace":       {Summary: "Get one trace with all its spans"},
	"get-trace-stats": {Summary: "Get the trace store's and the trace export's counts"},
}

var traceIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Source is what the admin routes read; *Provider is one.
type Source interface {
	Stats() TraceStats
	Store() *Store
}

type listIn struct {
	Limit  int    `query:"limit" minimum:"1" maximum:"500" default:"50"`
	Live   bool   `query:"live" doc:"only traces with a span still running"`
	Device string `query:"device" format:"uuid" doc:"only traces a span of names this device"`
	Since  uint64 `query:"since" doc:"only traces changed after this seq: the seq of the reader's last page or live event"`
}

type listOut struct{ Body TracePage }

type traceIn struct {
	ID string `path:"id" pattern:"^[0-9a-f]{32}$" doc:"the trace id, 32 lowercase hex digits"`
}

type traceOut struct{ Body TraceView }

type statsOut struct{ Body TraceStats }

// RegisterAdmin adds GET /traces, /traces/stats and /traces/{id}, admin's
// alone: span names and attributes describe what the owner's devices did.
func RegisterAdmin(h huma.API, src Source) {
	defer api.Describe(h, docs)
	op := func(id, path string) huma.Operation {
		return huma.Operation{OperationID: id, Method: http.MethodGet, Path: path, Tags: []string{tag}}
	}
	huma.Register(h, op("list-traces", "/traces"), func(_ context.Context, in *listIn) (*listOut, error) {
		page := src.Store().List(ListQuery{Limit: in.Limit, Live: in.Live, DeviceID: in.Device, Since: in.Since})
		return &listOut{Body: page}, nil
	})
	// before /traces/{id}: the router takes the first route that matches
	huma.Register(h, op("get-trace-stats", "/traces/stats"), func(context.Context, *struct{}) (*statsOut, error) {
		return &statsOut{Body: src.Stats()}, nil
	})
	huma.Register(h, op("get-trace", "/traces/{id}"), func(_ context.Context, in *traceIn) (*traceOut, error) {
		if !traceIDRe.MatchString(in.ID) {
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid", "/path/id")
		}
		t, ok := src.Store().Trace(in.ID)
		if !ok {
			return nil, api.Error(http.StatusNotFound, "not_found")
		}
		return &traceOut{Body: t}, nil
	})
}
