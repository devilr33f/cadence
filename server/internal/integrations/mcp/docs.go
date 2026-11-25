package mcp

import "git.shino.boo/devilreef/cadence/server/internal/api"

// PatchToolRequest changes the overrides it names; acknowledge, when given,
// must be the tool latest hash or nothing applies (409 hash_mismatch).
type PatchToolRequest struct {
	Acknowledge    *string `json:"acknowledge,omitempty" doc:"the tool latest hash"`
	AnyTurn        *bool   `json:"anyTurn,omitempty"`
	Effects        *string `json:"effects,omitempty" enum:"read,write"`
	EnabledDefault *bool   `json:"enabledDefault,omitempty"`
	Trust          *string `json:"trust,omitempty" enum:"trusted,untrusted"`
}

// AcknowledgeRequest names the tools to acknowledge at their hashes, or all
// changed ones; exactly one of the two keys.
type AcknowledgeRequest struct {
	Hashes map[string]string `json:"hashes,omitempty" doc:"remote tool name to its hash"`
	All    *bool             `json:"all,omitempty" doc:"true: every changed tool"`
}

var docs = map[string]api.Doc{
	"list-mcp-servers":      {Summary: "List the MCP servers"},
	"refresh-mcp-server":    {Summary: "Ask an MCP server for its tools again"},
	"list-mcp-tools":        {Summary: "List the tools of an MCP server"},
	"patch-mcp-tool":        {Summary: "Change the overrides of a tool", Body: PatchToolRequest{}, BodyRequired: true},
	"acknowledge-mcp-tools": {Summary: "Acknowledge the changed tools of a server", Body: AcknowledgeRequest{}, BodyRequired: true},
}
