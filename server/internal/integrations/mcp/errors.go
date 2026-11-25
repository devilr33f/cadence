package mcp

import (
	"errors"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Error is a failure code. It never carries remote text, the URL, a header or
// a session id, so it may reach any log or caller.
type Error struct {
	Code string
	// brk: the session can serve no new request; lost: the server no longer knows it
	brk, lost bool
	// unsent: the request never left, as without a session
	unsent bool
}

func (e *Error) Error() string { return e.Code }

// ErrorCode lets a caller pass the code on.
func (e *Error) ErrorCode() string { return e.Code }

const (
	// CodeUnavailable: not connected, transport, TLS, a refused redirect, a 5xx or 429, a dropped session
	CodeUnavailable = "mcp_unavailable"
	CodeTimeout     = "mcp_timeout"
	// CodeAuth: the server answered 401 or 403
	CodeAuth = "mcp_auth"
	// CodeProtocol: a body over the cap, a request outside the endpoint, a JSON-RPC error on
	// connect or list, a listing over its caps, a call result that asks for input
	CodeProtocol = "mcp_protocol"
	// CodeUnknownTool: a call refused with JSON-RPC invalid params, the spec's code for an unknown tool
	CodeUnknownTool = "mcp_unknown_tool"
	// CodeToolError: a call result with isError, or any other JSON-RPC error on a call
	CodeToolError = "mcp_tool_error"
	// CodeTooLarge: a Write tool ran, but its result is over the instance's cap
	CodeTooLarge = "mcp_too_large"
)

func fail(code string) *Error { return &Error{Code: code} }

// Code is err's code, or "" when err is no *Error.
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// notSent is whether err failed a request before it left.
func notSent(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.unsent
}

// breaking is err's code when it leaves the session unable to serve new
// requests.
func breaking(err error) (e *Error, ok bool) {
	return e, errors.As(err, &e) && e.brk
}

type op int

const (
	opConnect op = iota
	opList
	opCall
)

// classify names an SDK failure by the error's type, never its text, which
// quotes URLs, session ids and remote text. A transient status (the SDK
// keeps the connection), a 403 on a call and a server's JSON-RPC error leave
// the session in service.
func classify(err error, o op) *Error {
	var ue *url.Error
	var rpc *jsonrpc.Error
	switch {
	case errors.Is(err, errUnauthorized):
		return &Error{Code: CodeAuth, brk: true}
	case errors.Is(err, errForbidden):
		return &Error{Code: CodeAuth, brk: o != opCall}
	case errors.Is(err, errTransient):
		return fail(CodeUnavailable)
	case errors.Is(err, errEndpoint):
		return &Error{Code: CodeProtocol, brk: true}
	case errors.Is(err, errSessionGone), errors.Is(err, sdk.ErrSessionMissing):
		return &Error{Code: CodeUnavailable, brk: true, lost: true}
	case errors.As(err, &ue):
		// the request failed to reach the server: transport, TLS, a refused redirect
		return &Error{Code: CodeUnavailable, brk: true}
	case errors.As(err, &rpc):
		// the SDK's own sentinels share this type, but its transport failures
		// all carry a url.Error matched above, so this one is the server's,
		// whatever its code
		switch {
		case o != opCall:
			return fail(CodeProtocol)
		case rpc.Code == jsonrpc.CodeInvalidParams:
			return fail(CodeUnknownTool)
		}
		return fail(CodeToolError)
	}
	// the SDK failed the connection: a status it cannot use, a body it could not read or decode
	return &Error{Code: CodeUnavailable, brk: true}
}
