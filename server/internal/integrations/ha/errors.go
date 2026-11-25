package ha

import (
	"errors"
	"regexp"
	"strings"
)

// Error is a failure code. It never carries Home Assistant's text, the token
// or a URL, so it may reach any log or caller.
type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }

// Is matches an *Error with the same code; ErrHA matches every ha_error code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	if t.Code == codeHA {
		return strings.HasPrefix(e.Code, haErrorPrefix)
	}
	return t.Code == e.Code
}

const (
	CodeUnavailable = "unavailable"
	CodeTimeout     = "timeout"
	CodeAuthFailed  = "auth_failed"
	CodeBadResponse = "bad_response"

	codeHA        = "ha_error"
	haErrorPrefix = codeHA + ":"
)

// errors.Is targets for the codes
var (
	ErrUnavailable = &Error{Code: CodeUnavailable}
	ErrTimeout     = &Error{Code: CodeTimeout}
	ErrAuthFailed  = &Error{Code: CodeAuthFailed}
	ErrBadResponse = &Error{Code: CodeBadResponse}
	// ErrHA matches a command Home Assistant refused: ha_error:<its code>.
	ErrHA = &Error{Code: codeHA}
)

var haCodeRe = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

func fail(code string) *Error { return &Error{Code: code} }

// haError keeps Home Assistant's code only when it is a plain token.
func haError(code string) *Error {
	if !haCodeRe.MatchString(code) {
		code = "unknown"
	}
	return fail(haErrorPrefix + code)
}

// Code is err's code, or "" when err is no *Error.
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
