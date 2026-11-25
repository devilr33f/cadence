package api

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// Doc documents one operation after huma registered it. A route that reads its
// own body (RawJSON) or query (RawQuery) shows neither to huma, so what it
// accepts is declared here; only the document changes, never how a request is
// parsed or validated.
type Doc struct {
	Summary string
	// Body is a zero value of the type that describes the json body; Required
	// says an empty body is refused
	Body         any
	BodyRequired bool
	BodyNote     string
	Query        []Query
	// Reply replaces the schema of the success response, for a handler that
	// writes its own bytes
	Reply any
	// Scope is the scope a caller needs besides admin (M7a-D6); empty, the
	// operation is admin's alone
	Scope auth.Scope
}

// Query is one query parameter of a RawQuery route.
type Query struct {
	Name   string
	Note   string
	Schema *huma.Schema
}

// Describe applies the docs to the operations registered on a, by operation id.
// An id that is not registered, or a doc with no summary, panics at startup so
// a rename cannot leave a stale doc behind.
func Describe(a huma.API, docs map[string]Doc) {
	ops := map[string]*huma.Operation{}
	for _, item := range a.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Get, item.Put, item.Post, item.Delete, item.Patch, item.Head, item.Options, item.Trace} {
			if op != nil {
				ops[op.OperationID] = op
			}
		}
	}
	reg := a.OpenAPI().Components.Schemas
	for id, d := range docs {
		op, ok := ops[id]
		if !ok || d.Summary == "" {
			panic(fmt.Sprintf("api: doc for %q: not registered or no summary", id))
		}
		op.Summary = d.Summary
		if d.Scope != "" {
			if !slices.Contains(auth.KnownScopes, d.Scope) {
				panic(fmt.Sprintf("api: doc for %q: unknown scope", id))
			}
			declareScope(op, d.Scope)
		}
		if d.Body != nil {
			op.RequestBody = &huma.RequestBody{
				Description: d.BodyNote,
				Required:    d.BodyRequired,
				Content: map[string]*huma.MediaType{
					"application/json": {Schema: reg.Schema(reflect.TypeOf(d.Body), true, "")},
				},
			}
		}
		for _, q := range d.Query {
			op.Parameters = append(op.Parameters, &huma.Param{Name: q.Name, In: "query", Description: q.Note, Schema: q.Schema})
		}
		if d.Reply != nil {
			status := op.DefaultStatus
			if status == 0 {
				status = http.StatusOK
			}
			resp := op.Responses[strconv.Itoa(status)]
			if resp == nil {
				panic(fmt.Sprintf("api: doc for %q: no %d response", id, status))
			}
			// the content type is under content
			delete(resp.Headers, "Content-Type")
			resp.Content = map[string]*huma.MediaType{
				"application/json": {Schema: reg.Schema(reflect.TypeOf(d.Reply), true, "")},
			}
		}
	}
}

// Int is an integer query value within [lo, hi].
func Int(lo, hi int) *huma.Schema {
	l, h := float64(lo), float64(hi)
	return &huma.Schema{Type: huma.TypeInteger, Minimum: &l, Maximum: &h}
}

// MinInt is an integer query value of at least lo.
func MinInt(lo int) *huma.Schema {
	l := float64(lo)
	return &huma.Schema{Type: huma.TypeInteger, Minimum: &l}
}

// Text is a string query value of at most limit characters.
func Text(limit int) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, MaxLength: &limit}
}

// Enum is a string query value that is one of the values.
func Enum(values ...string) *huma.Schema {
	s := &huma.Schema{Type: huma.TypeString}
	for _, v := range values {
		s.Enum = append(s.Enum, v)
	}
	return s
}

// UUID is a string query value in uuid form.
func UUID() *huma.Schema { return &huma.Schema{Type: huma.TypeString, Format: "uuid"} }
