package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humafiber"
)

// RawQuery, embedded in an operation's input, hands the handler the query as
// sent, so a route keeps its own checks and refuses a parameter it does not
// take (huma ignores unknown ones and would answer its own text for a bad one).
// A query that does not parse (a bad escape, a ';') is a 422 naming the first
// bad key. It reads the request's raw query, never huma's URL(), which
// dereferences nil for a path with a bad escape.
type RawQuery struct{ q url.Values }

// Resolve reads the query.
func (r *RawQuery) Resolve(ctx huma.Context) []error {
	raw := string(humafiber.Unwrap(ctx).RequestCtx().URI().QueryString())
	q, err := url.ParseQuery(raw)
	if err != nil {
		return []error{Error(http.StatusUnprocessableEntity, "invalid", badKey(raw))}
	}
	r.q = q
	return nil
}

// Values is the query.
func (r *RawQuery) Values() url.Values { return r.q }

// badKey is the first pair's key that does not parse, as sent when its own
// escape is the bad part.
func badKey(raw string) string {
	for pair := range strings.FieldsFuncSeq(raw, func(r rune) bool { return r == '&' }) {
		key, val, _ := strings.Cut(pair, "=")
		_, kerr := url.QueryUnescape(key)
		_, verr := url.QueryUnescape(val)
		if kerr != nil || verr != nil || strings.Contains(pair, ";") {
			if kerr != nil {
				return key
			}
			k, _ := url.QueryUnescape(key)
			return strings.Trim(k, ";")
		}
	}
	return ""
}
