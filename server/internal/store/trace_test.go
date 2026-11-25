package store_test

import (
	"context"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/store"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
	"git.shino.boo/devilreef/cadence/server/internal/tracing/tracingtest"
)

// a query under a span is a child named by its sqlc name, never its text;
// one outside any trace records nothing
func TestQuerySpans(t *testing.T) {
	p := tracingtest.Setup(t)
	pool, err := store.Open(context.Background(), storetest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	ctx, root := tracing.Start(context.Background(), "test", "root")
	if err := pool.QueryRow(ctx, "-- name: CountSecretThings :one\nSELECT 'secret-text'::text, 2").Scan(new(string), &n); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT 'plain'`); err != nil {
		t.Fatal(err)
	}
	root.End()
	spans := tracingtest.Wait(t, p, root.SpanContext().TraceID().String(), "root", "db.CountSecretThings", "db.sql")
	if len(spans) != 3 {
		t.Fatalf("spans %+v", spans)
	}
	for _, s := range spans {
		for k, v := range s.Attrs {
			if str, ok := v.(string); ok && (str == "secret-text" || len(str) > 40) {
				t.Fatalf("%s %s=%v", s.Name, k, v)
			}
		}
	}
	if got := len(p.Store().List(tracing.ListQuery{}).Traces); got != 1 {
		t.Fatalf("%d traces: a query outside a trace recorded one", got)
	}
}
