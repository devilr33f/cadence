package mcpserver

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const tag = "mcpserver"

var docs = map[string]api.Doc{
	"list-mcp-endpoint-tools": {Summary: "The MCP endpoint's own tools: their names and descriptions, the whole toolset a token may name"},
}

type toolsOut struct{ Body []Tool }

// RegisterAdmin mounts the endpoint's admin route: the whole toolset as the
// tokens' editor lists it, never one caller's filtered view.
func RegisterAdmin(a huma.API, s *Server) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "list-mcp-endpoint-tools", Method: http.MethodGet, Path: "/mcp/tools", Tags: []string{tag},
	}, func(_ context.Context, _ *struct{}) (*toolsOut, error) {
		return &toolsOut{Body: s.Tools()}, nil
	})
}
