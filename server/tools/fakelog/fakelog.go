// Package fakelog logs a dev fake's requests by method, the route's first
// segment and status: never a path's ids, a query or a body (rule 8).
package fakelog

import (
	"log/slog"
	"net/http"
	"strings"
)

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(s int) { r.status = s; r.ResponseWriter.WriteHeader(s) }

// Routes wraps h.
func Routes(log *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		seg := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 4)
		route := "/" + seg[0]
		if seg[0] == "api" && len(seg) > 2 {
			route = "/api/v1/" + seg[2]
		}
		log.Info("request", "method", r.Method, "route", route, "status", rec.status)
	})
}
