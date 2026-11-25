package api

import (
	"context"
	"log/slog"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humafiber"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const (
	// scopeKey holds an operation's required scope in its metadata
	scopeKey = "cadence.scope"
	// ScopeExtension names an operation's required scope in the document
	ScopeExtension = "x-cadence-scope"
)

// RequiredScope is the scope an operation needs: the one its doc declares,
// else admin, so a new operation is admin-only until someone widens it.
func RequiredScope(op *huma.Operation) auth.Scope {
	if s, ok := op.Metadata[scopeKey].(auth.Scope); ok && s != "" {
		return s
	}
	return auth.ScopeAdmin
}

func declareScope(op *huma.Operation, s auth.Scope) {
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[scopeKey] = s
}

// public is login or the session state, which answer without credentials
func public(op *huma.Operation) bool { return op.Security != nil && len(op.Security) == 0 }

// documentScopes names each operation's scope in the document, so a client
// sees what a token needs.
func documentScopes(a huma.API) {
	for _, item := range a.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Get, item.Put, item.Post, item.Delete, item.Patch, item.Head, item.Options, item.Trace} {
			if op == nil || public(op) {
				continue
			}
			if op.Extensions == nil {
				op.Extensions = map[string]any{}
			}
			op.Extensions[ScopeExtension] = string(RequiredScope(op))
		}
	}
}

// scopeGate refuses a caller whose scopes do not allow the operation (M7a-D6);
// login and the session state need none: a request the auth middleware let
// through as one of them, reaching an operation documented as open.
func scopeGate(a huma.API, log *slog.Logger) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		st, _ := humafiber.Unwrap(ctx).Locals(stateKey{}).(*authState)
		switch need := RequiredScope(ctx.Operation()); {
		case st != nil && st.public && public(ctx.Operation()):
			next(ctx)
		case st == nil || st.p == nil:
			_ = huma.WriteErr(a, ctx, http.StatusUnauthorized, "")
		case !st.p.scopes.Allows(need):
			log.Warn("api_forbidden", "op", ctx.Operation().OperationID, "scope", string(need))
			_ = huma.WriteErr(a, ctx, http.StatusForbidden, "")
		default:
			next(ctx)
		}
	}
}

// requireScope guards a plain route: the caller needs one of scopes (admin
// has them all; none named: admin only). op names the route in the refusal's
// log line.
func requireScope(op string, log *slog.Logger, scopes ...auth.Scope) fiber.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if len(scopes) == 0 {
		scopes = auth.AdminScopes
	}
	return func(c fiber.Ctx) error {
		have := CallerScopes(c)
		if have == nil {
			return refuse(c, fiber.StatusUnauthorized, "unauthorized")
		}
		if !slices.ContainsFunc(scopes, have.Allows) {
			log.Warn("api_forbidden", "op", op, "scope", string(scopes[0]))
			return refuse(c, fiber.StatusForbidden, "forbidden")
		}
		return c.Next()
	}
}

// Locals is a request's locals: a fiber.Ctx, or a websocket made from one.
type Locals interface {
	Locals(key any, value ...any) any
}

// CallerScopes are the scopes the auth middleware found on a request: nil for
// an anonymous one, admin for the owner's session.
func CallerScopes(l Locals) auth.Scopes {
	st, _ := l.Locals(stateKey{}).(*authState)
	if st == nil || st.p == nil {
		return nil
	}
	return st.p.scopes
}

// CallerGrants are the caller's whole grants, its scopes and the MCP tools
// its token names (every tool when it names none).
func CallerGrants(l Locals) auth.Grants {
	st, _ := l.Locals(stateKey{}).(*authState)
	if st == nil || st.p == nil {
		return auth.Grants{}
	}
	return auth.Grants{Scopes: st.p.scopes, MCPTools: st.p.mcpTools}
}

// CallerRecheck asks the store again whether a request's credential still
// holds with the scopes it had, for a connection that outlives the request
// (the live feed): nil while it does, auth.ErrUnauthorized once the token is
// gone or its scopes changed, or the session ended (logout, expiry, idleness, a
// password change), another error when the store could not tell. It is nil for
// an anonymous request.
func CallerRecheck(l Locals) func(context.Context) error {
	st, _ := l.Locals(stateKey{}).(*authState)
	if st == nil || st.p == nil {
		return nil
	}
	return st.p.recheck
}
