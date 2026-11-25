package auth

import (
	"errors"
	"slices"
)

// Scope is what an API token may do (M7a-D6). admin may do everything; ha
// reads devices, their media and vitals and controls them; metrics reads the
// metrics; presence reads, sets and clears busy signals; mcp calls the MCP
// endpoint, with the tool list the token names (none named: every tool). An admin session (the owner's cookie) is admin.
type Scope string

const (
	ScopeAdmin    Scope = "admin"
	ScopeHA       Scope = "ha"
	ScopeMetrics  Scope = "metrics"
	ScopePresence Scope = "presence"
	ScopeMCP      Scope = "mcp"
)

// KnownScopes are every scope, in the order a token lists them.
var KnownScopes = []Scope{ScopeAdmin, ScopeHA, ScopeMetrics, ScopePresence, ScopeMCP}

// ErrScopeInvalid refuses an unknown scope, or none.
var ErrScopeInvalid = errors.New("auth: unknown or no scope")

// Scopes are what one caller may do.
type Scopes []Scope

// AdminScopes are an admin token's and the owner's session's.
var AdminScopes = Scopes{ScopeAdmin}

// ParseScopes reads scope names, each once and in KnownScopes order; an
// unknown name, or none, is ErrScopeInvalid.
func ParseScopes(names []string) (Scopes, error) {
	if len(names) == 0 {
		return nil, ErrScopeInvalid
	}
	for _, n := range names {
		if !slices.Contains(KnownScopes, Scope(n)) {
			return nil, ErrScopeInvalid
		}
	}
	var s Scopes
	for _, k := range KnownScopes {
		if slices.Contains(names, string(k)) {
			s = append(s, k)
		}
	}
	return s, nil
}

// Allows says whether s may do what need requires: admin may do everything,
// another scope only its own, and an empty need is admin's alone.
func (s Scopes) Allows(need Scope) bool {
	if slices.Contains(s, ScopeAdmin) {
		return true
	}
	return need != "" && slices.Contains(KnownScopes, need) && slices.Contains(s, need)
}

// Strings are the scopes' names.
func (s Scopes) Strings() []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = string(v)
	}
	return out
}
