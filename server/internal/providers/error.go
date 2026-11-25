package providers

import "errors"

// provider failure codes that tell the owner what to do. CallError carries
// only these codes and an HTTP status, never an upstream response body.
const (
	CodeProviderError = "provider_error"
	CodeRateLimited   = "rate_limited"
	CodeAuth          = "auth"
)

// CallError is a provider call's safe failure. Error returns Code alone.
type CallError struct {
	Code   string
	Status int
}

func (e *CallError) Error() string {
	if e == nil || !safeCode(e.Code) {
		return CodeProviderError
	}
	return e.Code
}

func (e *CallError) ErrorCode() string { return e.Error() }

// HTTPStatusCode lets the agent retain an upstream status without reading an
// error's text, which may echo a prompt or a credential.
func (e *CallError) HTTPStatusCode() int {
	if e == nil {
		return 0
	}
	return e.Status
}

// ErrorFacts reads only explicit interfaces. An arbitrary error's Error()
// string is never a failure code.
func ErrorFacts(err error, fallback string) (code string, httpStatus int) {
	code = fallback
	if !safeCode(code) {
		code = CodeProviderError
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) && safeCode(coded.ErrorCode()) {
		code = coded.ErrorCode()
	}
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) {
		httpStatus = status.HTTPStatusCode()
	}
	return code, httpStatus
}

func safeCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for _, r := range code {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// SafeError preserves a CallError's code and status and replaces every other
// error with fallback, so no upstream error text crosses the provider boundary.
func SafeError(err error, fallback string) error {
	code, status := ErrorFacts(err, fallback)
	return &CallError{Code: code, Status: status}
}
