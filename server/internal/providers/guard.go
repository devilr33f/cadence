package providers

import "log/slog"

// Guard runs fn and recovers a panic; the value can carry upstream data, so
// only the module and the code are logged. A nil log falls back to slog's default.
func Guard(log *slog.Logger, module, code string, fn func()) (failed bool) {
	defer func() {
		if recover() != nil {
			if log == nil {
				log = slog.Default()
			}
			log.Error("provider_panic", "module", module, "code", code)
			failed = true
		}
	}()
	fn()
	return false
}
