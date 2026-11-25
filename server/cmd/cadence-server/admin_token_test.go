package main

import (
	"bytes"
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/store"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

// a bad name or scope is refused before the database is reached
func TestAdminTokenCreateRefusesBeforeTheDatabase(t *testing.T) {
	env := envOf(map[string]string{"DATABASE_URL": "postgres://u:p@127.0.0.1:1/x?connect_timeout=1", "CADENCE_MASTER_KEY_FILE": keyFile(t)})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--name", "Bad Name"}, "token name:"},
		{[]string{"--name", "ci", "--scope", "root"}, "scope:"},
		{[]string{"--name", "ci", "--scope", "ha", "--scope", ""}, "scope:"},
		{[]string{"--name", "ci", "--scope"}, "usage:"},
		{[]string{"--name", "ci", "extra"}, "usage:"},
		{[]string{"--name", "ci", "--bogus", "x"}, "usage:"},
	} {
		var out, errb bytes.Buffer
		if code := runAdmin(append([]string{"token", "create"}, tc.args...), env, strings.NewReader(""), &out, &errb); code != 2 {
			t.Fatalf("%v: code %d, stderr %s", tc.args, code, errb.String())
		}
		if out.Len() != 0 || !strings.Contains(errb.String(), tc.want) || strings.Contains(errb.String(), "db_unavailable") {
			t.Fatalf("%v: stdout %q stderr %q", tc.args, out.String(), errb.String())
		}
	}
}

// --scope repeats and defaults to admin; the token is printed once, on stdout
func TestAdminTokenCreateScopes(t *testing.T) {
	ctx := context.Background()
	dsn := storetest.URL(t)
	pool, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	env := envOf(map[string]string{"DATABASE_URL": dsn, "CADENCE_MASTER_KEY_FILE": keyFile(t)})
	tokens := auth.NewTokens(db.New(pool), time.Now)
	for _, tc := range []struct {
		args []string
		want auth.Scopes
	}{
		{[]string{"--name", "owner"}, auth.AdminScopes},
		{[]string{"--name", "home-assistant", "--scope", "ha", "--scope", "metrics"}, auth.Scopes{auth.ScopeHA, auth.ScopeMetrics}},
		{[]string{"--scope=ha", "--name=ha.2"}, auth.Scopes{auth.ScopeHA}},
	} {
		var out, errb bytes.Buffer
		if code := runAdmin(append([]string{"token", "create"}, tc.args...), env, strings.NewReader(""), &out, &errb); code != 0 {
			t.Fatalf("%v: code %d, stderr %s", tc.args, code, errb.String())
		}
		tok := strings.TrimSuffix(out.String(), "\n")
		if !regexp.MustCompile(`^cdn_[a-z2-7]{52}$`).MatchString(tok) || strings.Contains(errb.String(), tok) {
			t.Fatalf("%v: stdout %d bytes, stderr %s", tc.args, out.Len(), errb.String())
		}
		if got, err := tokens.Verify(ctx, tok); err != nil || !slices.Equal(got.Scopes, tc.want) {
			t.Fatalf("%v: scopes %v %v", tc.args, got, err)
		}
	}
	var out, errb bytes.Buffer
	if code := runAdmin([]string{"token", "create", "--name", "owner"}, env, strings.NewReader(""), &out, &errb); code != 1 || out.Len() != 0 ||
		!strings.Contains(errb.String(), `"msg":"token_create_failed","code":"name_taken"`) {
		t.Fatalf("taken name: code %d stdout %q stderr %s", code, out.String(), errb.String())
	}
}
