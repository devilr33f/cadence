package hermes

import (
	"errors"
	"time"
)

// Error is a failure code. It never carries upstream text, so it may reach any
// log or caller.
type Error struct {
	Code string
	// retryAfter is the server's Retry-After, capped
	retryAfter time.Duration
}

func (e *Error) Error() string { return e.Code }

// ErrorCode lets the task manager pass the code on.
func (e *Error) ErrorCode() string { return e.Code }

// Codes returned by the client and the kind, and carried by task events.
const (
	CodeUnavailable  = "hermes_unavailable"
	CodeTimeout      = "hermes_timeout"
	CodeRefused      = "hermes_refused"
	CodeIncompatible = "hermes_incompatible"
	CodeConflict     = "hermes_conflict"
	CodeGone         = "hermes_gone"
	CodeUnknown      = "hermes_unknown"

	CodeBadInput         = "hermes_bad_input"
	CodeBadDecision      = "hermes_bad_decision"
	CodeNotFound         = "hermes_not_found"
	CodeStore            = "hermes_store_failed"
	CodeBusy             = "hermes_busy"
	CodeNoQuestion       = "hermes_no_question"
	CodeApprovalPending  = "hermes_approval_pending"
	CodeNoApproval       = "hermes_no_approval"
	CodePromptUnshowable = "hermes_prompt_unshowable"
	CodeSecretPrompt     = "hermes_secret_prompt"
	CodeCapacity         = "hermes_capacity"
	CodeStale            = "hermes_stale"
	CodeFinished         = "hermes_finished"

	CodeFailed      = "hermes_failed"
	CodeInterrupted = "hermes_interrupted"
	CodeRunGone     = "hermes_run_gone"
	CodeRunUnknown  = "hermes_run_unknown"
	CodeUnconfirmed = "hermes_unconfirmed"
	CodeStopUnknown = "hermes_stop_unknown"
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

func retryAfter(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) {
		return e.retryAfter
	}
	return 0
}

// transient errors may succeed on a retry with the same request
func transient(err error) bool {
	c := Code(err)
	return c == CodeUnavailable || c == CodeTimeout
}
