package apitest

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// HABearer is an ha token to Verify.
const HABearer = "ha-token"

// MCPBearer is an mcp token to Verify, one that sees every tool.
const MCPBearer = "mcp-token"

// Verify is a rig's token check: HABearer is an ha token, MCPBearer an mcp
// one, any other bearer an admin one.
func Verify(_ context.Context, bearer string) (auth.Grants, error) {
	switch bearer {
	case HABearer:
		return auth.Grants{Scopes: auth.Scopes{auth.ScopeHA}}, nil
	case MCPBearer:
		return auth.Grants{Scopes: auth.Scopes{auth.ScopeMCP}}, nil
	}
	return auth.Grants{Scopes: auth.AdminScopes}, nil
}

// Only takes bearer alone, as an admin token.
func Only(bearer string) func(context.Context, string) (auth.Grants, error) {
	return func(_ context.Context, got string) (auth.Grants, error) {
		if got != bearer {
			return auth.Grants{}, auth.ErrUnauthorized
		}
		return auth.Grants{Scopes: auth.AdminScopes}, nil
	}
}

// Refuse takes no bearer.
func Refuse(context.Context, string) (auth.Grants, error) {
	return auth.Grants{}, auth.ErrUnauthorized
}
