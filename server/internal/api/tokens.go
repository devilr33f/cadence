package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const tokensTag = "tokens"

// TokenStore keeps the API tokens (auth.Tokens).
type TokenStore interface {
	List(ctx context.Context) ([]auth.TokenInfo, error)
	Create(ctx context.Context, name string, scopes, mcpTools []string) (string, auth.Scopes, error)
	SetMCPTools(ctx context.Context, name string, tools []string) error
	Delete(ctx context.Context, name string) error
}

// TokenView is an API token as listed: never the token or its hash.
type TokenView struct {
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes" nullable:"false" enum:"admin,ha,metrics,presence,mcp"`
	MCPTools   []string   `json:"mcpTools,omitempty" doc:"The MCP tools an mcp token sees, in the order stored; absent: every tool."`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty" doc:"When the token was last used, to the minute; absent until its first use."`
}

// CreateTokenRequest names a new API token and what it may do.
type CreateTokenRequest struct {
	Name   string   `json:"name" pattern:"^[a-z0-9][a-z0-9._-]{0,63}$" doc:"Unique."`
	Scopes []string `json:"scopes" nullable:"false" minItems:"1" uniqueItems:"true" enum:"admin,ha,metrics,presence,mcp" doc:"admin may do everything; ha reads devices, their media and vitals and controls them; metrics reads the metrics; presence sets and clears busy signals; mcp calls the MCP endpoint."`
	// MCPTools names the tools an mcp token sees; absent: every tool
	MCPTools []string `json:"mcpTools,omitempty" uniqueItems:"true" doc:"The MCP tools the token sees, by name; absent or empty: every tool. Only read with the mcp scope."`
}

// TokenToolsRequest changes an mcp token's tool list.
type TokenToolsRequest struct {
	MCPTools []string `json:"mcpTools" uniqueItems:"true" doc:"The MCP tools the token sees, by name; empty: every tool."`
}

// CreatedToken is a new API token: the only time its value is shown.
type CreatedToken struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes" nullable:"false" enum:"admin,ha,metrics,presence,mcp"`
	Token  string   `json:"token" doc:"The bearer token; shown this once, stored as its hash only."`
}

type tokenListOut struct{ Body []TokenView }

type tokenCreateIn struct{ Body CreateTokenRequest }

// the one answer that carries a token is never cached
type tokenCreateOut struct {
	CacheControl string `header:"Cache-Control"`
	Body         CreatedToken
}

type tokenNameIn struct {
	Name string `path:"name"`
}

type tokenToolsIn struct {
	Name string `path:"name"`
	Body TokenToolsRequest
}

var tokenDocs = map[string]Doc{
	"list-tokens":  {Summary: "List API tokens: names, scopes and use, never a token"},
	"create-token": {Summary: "Create an API token: its value is answered this once"},
	"token-tools":  {Summary: "Change an API token's MCP tool list"},
	"delete-token": {Summary: "Delete an API token"},
}

// RegisterTokens mounts the API tokens' management (M7a-D6), admin's alone.
// knownMCPTools, when not nil, is the MCP tool names a token may name; nil
// takes any well-formed name (a rig without the MCP server).
func RegisterTokens(a huma.API, s TokenStore, knownMCPTools func() []string, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	defer Describe(a, tokenDocs)
	// the cause can carry driver text, so only the operation is logged
	failed := func(op string) error {
		log.Error("api_tokens_failed", "op", op)
		return Error(http.StatusInternalServerError, "internal")
	}
	// an unknown tool name is refused before the store, so a typo cannot
	// silently hide a tool from a token
	known := func(tools []string) error {
		if knownMCPTools == nil {
			_, err := auth.ParseMCPTools(tools)
			return err
		}
		names := knownMCPTools()
		for _, t := range tools {
			if !slices.Contains(names, t) {
				return errUnknownTool
			}
		}
		_, err := auth.ParseMCPTools(tools)
		return err
	}
	huma.Register(a, huma.Operation{
		OperationID: "list-tokens", Method: http.MethodGet, Path: "/tokens", Tags: []string{tokensTag},
	}, func(ctx context.Context, _ *struct{}) (*tokenListOut, error) {
		list, err := s.List(ctx)
		if err != nil {
			return nil, failed("list")
		}
		out := &tokenListOut{Body: make([]TokenView, 0, len(list))}
		for _, t := range list {
			out.Body = append(out.Body, TokenView{Name: t.Name, Scopes: t.Scopes.Strings(), MCPTools: t.MCPTools, CreatedAt: t.CreatedAt.UTC(), LastUsedAt: utc(t.LastUsedAt)})
		}
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "create-token", Method: http.MethodPost, Path: "/tokens", Tags: []string{tokensTag},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *tokenCreateIn) (*tokenCreateOut, error) {
		switch err := known(in.Body.MCPTools); {
		case errors.Is(err, errUnknownTool):
			return nil, Error(http.StatusUnprocessableEntity, "unknown_mcp_tool", "/body/mcpTools")
		case err != nil:
			return nil, Error(http.StatusUnprocessableEntity, "invalid", "/body/mcpTools")
		}
		tok, scopes, err := s.Create(ctx, in.Body.Name, in.Body.Scopes, in.Body.MCPTools)
		switch {
		case errors.Is(err, auth.ErrTokenNameTaken):
			return nil, Error(http.StatusConflict, "name_taken")
		case errors.Is(err, auth.ErrTokenName):
			return nil, Error(http.StatusUnprocessableEntity, "invalid", "/body/name")
		case errors.Is(err, auth.ErrScopeInvalid):
			return nil, Error(http.StatusUnprocessableEntity, "invalid", "/body/scopes")
		case errors.Is(err, auth.ErrToolInvalid):
			return nil, Error(http.StatusUnprocessableEntity, "invalid", "/body/mcpTools")
		case err != nil:
			return nil, failed("create")
		}
		log.Info("api_token_created", "scopes", scopes.Strings(), "mcp_tools", len(in.Body.MCPTools))
		return &tokenCreateOut{CacheControl: "no-store", Body: CreatedToken{Name: in.Body.Name, Scopes: scopes.Strings(), Token: tok}}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "token-tools", Method: http.MethodPatch, Path: "/tokens/{name}", Tags: []string{tokensTag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *tokenToolsIn) (*struct{}, error) {
		// fiber hands the segment as sent: a name from before the pattern can
		// hold a space or a slash, which only its escaped form carries
		name, err := url.PathUnescape(in.Name)
		if err != nil {
			return nil, Error(http.StatusNotFound, "not_found")
		}
		switch err := known(in.Body.MCPTools); {
		case errors.Is(err, errUnknownTool):
			return nil, Error(http.StatusUnprocessableEntity, "unknown_mcp_tool", "/body/mcpTools")
		case err != nil:
			return nil, Error(http.StatusUnprocessableEntity, "invalid", "/body/mcpTools")
		}
		switch err := s.SetMCPTools(ctx, name, in.Body.MCPTools); {
		case errors.Is(err, auth.ErrTokenNotFound):
			return nil, Error(http.StatusNotFound, "not_found")
		case errors.Is(err, auth.ErrToolInvalid):
			return nil, Error(http.StatusUnprocessableEntity, "invalid", "/body/mcpTools")
		case err != nil:
			return nil, failed("tools")
		}
		log.Info("api_token_tools", "mcp_tools", len(in.Body.MCPTools))
		return &struct{}{}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-token", Method: http.MethodDelete, Path: "/tokens/{name}", Tags: []string{tokensTag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *tokenNameIn) (*struct{}, error) {
		// fiber hands the segment as sent: a name from before the pattern can
		// hold a space or a slash, which only its escaped form carries
		name, err := url.PathUnescape(in.Name)
		if err != nil {
			return nil, Error(http.StatusNotFound, "not_found")
		}
		switch err := s.Delete(ctx, name); {
		case errors.Is(err, auth.ErrTokenNotFound):
			return nil, Error(http.StatusNotFound, "not_found")
		case err != nil:
			return nil, failed("delete")
		}
		log.Info("api_token_deleted")
		return &struct{}{}, nil
	})
}

// errUnknownTool names an MCP tool the server does not serve.
var errUnknownTool = errors.New("api: unknown mcp tool")

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
