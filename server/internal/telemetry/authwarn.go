package telemetry

import (
	"log/slog"
	"sync"
	"time"
)

// authWarning logs a failed token lookup at most once a window, server-wide:
// the token store down fails every device's every request. The first failure
// is logged at once and opens a window; those within it are counted, and its
// end logs the count and opens the next one, until a window ends with none.
type authWarning struct {
	every time.Duration

	mu   sync.Mutex
	open bool
	held int
}

func (w *authWarning) fail(log *slog.Logger, kind string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.open {
		w.held++
		return
	}
	w.open = true
	// the cause can carry driver text, so only the code is logged
	log.Warn("otlp_ingest_failed", "kind", kind, "code", "auth")
	time.AfterFunc(w.every, func() { w.end(log) })
}

func (w *authWarning) end(log *slog.Logger) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.held == 0 {
		w.open = false
		return
	}
	log.Warn("otlp_ingest_failed", "code", "auth", "suppressed", w.held)
	w.held = 0
	time.AfterFunc(w.every, func() { w.end(log) })
}
