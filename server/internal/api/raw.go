package api

import (
	"io"

	"github.com/danielgtaylor/huma/v2"
)

// RawJSON, embedded in an operation's input, hands the handler its request
// body as sent. Huma reads no body for an input without Body or RawBody, so it
// neither parses, validates nor requires this one (its RawBody would require
// one): the route's own checks answer every body, an empty one included, with
// the codes they always had.
type RawJSON struct{ body []byte }

// Resolve reads the body; the slice is the input's own, so it outlives the request.
func (r *RawJSON) Resolve(ctx huma.Context) []error {
	b, err := io.ReadAll(ctx.BodyReader())
	if err != nil {
		return []error{&huma.ErrorDetail{Location: "body"}}
	}
	r.body = b
	return nil
}

// Bytes is the request body.
func (r *RawJSON) Bytes() []byte { return r.body }
