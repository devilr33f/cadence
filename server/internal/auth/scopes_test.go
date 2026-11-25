package auth_test

import (
	"errors"
	"slices"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

func TestParseScopes(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want auth.Scopes
	}{
		{[]string{"admin"}, auth.Scopes{auth.ScopeAdmin}},
		{[]string{"metrics", "ha", "ha"}, auth.Scopes{auth.ScopeHA, auth.ScopeMetrics}},
		{[]string{"ha", "admin"}, auth.Scopes{auth.ScopeAdmin, auth.ScopeHA}},
	} {
		got, err := auth.ParseScopes(tc.in)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%v: %v %v, want %v", tc.in, got, err, tc.want)
		}
	}
	for _, in := range [][]string{nil, {}, {"root"}, {"ha", "Admin"}, {""}, {"ha", "metrics", "x"}} {
		if got, err := auth.ParseScopes(in); !errors.Is(err, auth.ErrScopeInvalid) || got != nil {
			t.Errorf("%v: %v %v", in, got, err)
		}
	}
}

// admin may do everything, another scope only its own; a need left empty is
// admin's alone
func TestScopesAllow(t *testing.T) {
	admin, ha, metrics := auth.Scopes{auth.ScopeAdmin}, auth.Scopes{auth.ScopeHA}, auth.Scopes{auth.ScopeMetrics}
	for _, tc := range []struct {
		have  auth.Scopes
		need  auth.Scope
		allow bool
	}{
		{admin, auth.ScopeAdmin, true}, {admin, auth.ScopeHA, true}, {admin, auth.ScopeMetrics, true}, {admin, "", true},
		{ha, auth.ScopeHA, true}, {ha, auth.ScopeAdmin, false}, {ha, auth.ScopeMetrics, false}, {ha, "", false},
		{metrics, auth.ScopeMetrics, true}, {metrics, auth.ScopeHA, false},
		{auth.Scopes{auth.ScopeHA, auth.ScopeMetrics}, auth.ScopeMetrics, true},
		{nil, auth.ScopeHA, false}, {auth.Scopes{"root"}, "root", false},
		{auth.Scopes{auth.ScopePresence}, auth.ScopePresence, true}, {auth.Scopes{auth.ScopePresence}, auth.ScopeHA, false},
		{auth.Scopes{auth.ScopePresence}, "", false}, {admin, auth.ScopePresence, true}, {ha, auth.ScopePresence, false},
	} {
		if got := tc.have.Allows(tc.need); got != tc.allow {
			t.Errorf("%v allows %q: %v", tc.have, tc.need, got)
		}
	}
	if !slices.Equal(auth.KnownScopes, []auth.Scope{auth.ScopeAdmin, auth.ScopeHA, auth.ScopeMetrics, auth.ScopePresence, auth.ScopeMCP}) {
		t.Fatalf("known %v", auth.KnownScopes)
	}
}
