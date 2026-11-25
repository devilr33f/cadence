package mcpserver

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

// Fiber is the endpoint as the api's /mcp route handler (api.Options.MCP):
// it bridges the request to the streamable handler with the caller's token
// grants, which decide the tools this one request serves.
func (s *Server) Fiber() fiber.Handler {
	h := s.Endpoint()
	return func(c fiber.Ctx) error {
		caller := api.CallerGrants(c)
		list := make(map[string]bool, len(caller.MCPTools))
		for _, t := range caller.MCPTools {
			list[t] = true
		}
		g := grants{all: caller.AllTools(), list: list}
		body, _ := io.ReadAll(bytes.NewReader(c.Body()))
		req, err := http.NewRequest(c.Method(), "/", bytes.NewReader(body))
		if err != nil {
			return fiber.ErrInternalServerError
		}
		for k, v := range c.Request().Header.All() {
			req.Header.Set(string(k), string(v))
		}
		req = req.WithContext(withGrants(c.Context(), g))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		for k, vs := range rec.Header() {
			if k == "Content-Length" {
				continue
			}
			// the first value replaces fiber's default (a plain text
			// content type), the rest append
			for i, v := range vs {
				if i == 0 {
					c.Set(k, v)
				} else {
					c.Append(k, v)
				}
			}
		}
		return c.Status(rec.Code).Send(rec.Body.Bytes())
	}
}
