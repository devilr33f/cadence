package telemetry

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// a statement the server cut at its timeout reads as a timeout, never as the
// driver's text
func TestStatementTimeoutIsATimeout(t *testing.T) {
	err := storeError("list logs", &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout", Detail: "q=secret"})
	if !errors.Is(err, errReadTimeout) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("%v", err)
	}
	if err := storeError("list logs", &pgconn.PgError{Code: "23514"}); errors.Is(err, errReadTimeout) || !errors.Is(err, errStore) {
		t.Fatalf("%v", err)
	}
}
