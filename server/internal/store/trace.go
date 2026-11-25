package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// queryTracer gives each query made under a recording span a db.<Name> child
// named by its sqlc name (the generated statement's "-- name: X" line), never
// the statement or its arguments (rule 8). Queries outside a trace, the
// pool's own and every background loop's, record nothing.
type queryTracer struct{}

type querySpanKey struct{}

func (queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if !trace.SpanFromContext(ctx).IsRecording() {
		return ctx
	}
	ctx, span := tracing.StartKind(ctx, "db", "db."+queryName(d.SQL), trace.SpanKindClient,
		attribute.String("db.system", "postgresql"))
	return context.WithValue(ctx, querySpanKey{}, span)
}

func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	span, ok := ctx.Value(querySpanKey{}).(trace.Span)
	if !ok {
		return
	}
	span.SetAttributes(attribute.Int64("db.response.rows", d.CommandTag.RowsAffected()))
	if d.Err != nil && !errors.Is(d.Err, pgx.ErrNoRows) {
		tracing.FailErr(span, d.Err)
	}
	span.End()
}

// queryName is a sqlc statement's name, "sql" for any other
func queryName(sql string) string {
	const prefix = "-- name: "
	if !strings.HasPrefix(sql, prefix) {
		return "sql"
	}
	name, _, _ := strings.Cut(sql[len(prefix):], " ")
	if name == "" || len(name) > 64 {
		return "sql"
	}
	return name
}
