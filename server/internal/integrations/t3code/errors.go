package t3code

import (
	"context"
	"errors"
)

// failure codes: never T3's text, the URL or the token
const (
	// CodeUnavailable: no connection, or it ended before the answer
	CodeUnavailable = "unavailable"
	CodeTimeout     = "timeout"
	// CodeAuthFailed: the token (or the ticket it bought) was refused
	CodeAuthFailed = "auth_failed"
	// CodeForbidden: the token lacks the scope a call needs
	CodeForbidden = "forbidden"
	// CodeProtocol: the server does not speak orchestration protocol 2
	CodeProtocol    = "protocol_incompatible"
	CodeBadResponse = "bad_response"
	// CodeRefused: T3 answered the call with its own typed error, named by Error.Tag
	CodeRefused = "refused"
)

// Error is a call's failure: a code, and for refused the tag of T3's error
// (OrchestrationV2DispatchCommandError, ...), never its message.
type Error struct {
	Code string
	Tag  string
}

func (e *Error) Error() string {
	if e.Tag != "" {
		return "t3code: " + e.Code + ":" + e.Tag
	}
	return "t3code: " + e.Code
}

func fail(code string) error { return &Error{Code: code} }

// ErrorCode is err's code: "" for nil, an Error's own, timeout for a passed
// deadline, unavailable for a cancelled context, bad_response otherwise.
func ErrorCode(err error) string {
	var e *Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &e):
		return e.Code
	case errors.Is(err, context.DeadlineExceeded):
		return CodeTimeout
	case errors.Is(err, context.Canceled):
		return CodeUnavailable
	}
	return CodeBadResponse
}

// ErrBadURL names no part of the URL, which may carry credentials.
var ErrBadURL = errors.New("t3code: base url must be http or https, without userinfo, query, fragment or whitespace")

var errNoToken = errors.New("t3code: token missing")

// exitError maps a failed Exit to an Error.
func exitError(e *ExitValue) error {
	if e.interrupted() {
		return fail(CodeUnavailable)
	}
	switch tag := e.failTag(); tag {
	case "":
		return fail(CodeBadResponse)
	case "EnvironmentAuthorizationError":
		return &Error{Code: CodeForbidden, Tag: tag}
	default:
		return &Error{Code: CodeRefused, Tag: tag}
	}
}
