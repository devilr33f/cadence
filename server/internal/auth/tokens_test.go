package auth_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type tokensRig struct {
	tk   *auth.Tokens
	c    *clock
	pool *pgxpool.Pool
}

func newTokens(t *testing.T) *tokensRig {
	t.Helper()
	pool := storetest.Pool(t)
	c := newClock()
	return &tokensRig{tk: auth.NewTokens(db.New(pool), c.Now), c: c, pool: pool}
}

func (r *tokensRig) create(t *testing.T, name string, scopes ...string) string {
	t.Helper()
	tok, _, err := r.tk.Create(context.Background(), name, scopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestCreateThenVerify(t *testing.T) {
	ctx := context.Background()
	r := newTokens(t)
	tok, scopes, err := r.tk.Create(ctx, "home-assistant", []string{"metrics", "ha"}, nil)
	if err != nil || !slices.Equal(scopes, auth.Scopes{auth.ScopeHA, auth.ScopeMetrics}) {
		t.Fatalf("%v %v", scopes, err)
	}
	got, err := r.tk.Verify(ctx, tok)
	if err != nil || !slices.Equal(got.Scopes, auth.Scopes{auth.ScopeHA, auth.ScopeMetrics}) {
		t.Fatalf("%v %v", got, err)
	}
	var hash []byte
	if err := r.pool.QueryRow(ctx, "SELECT hash FROM api_tokens WHERE name = 'home-assistant'").Scan(&hash); err != nil ||
		string(hash) != string(auth.Hash(tok)) {
		t.Fatalf("stored %v", err)
	}
}

func TestVerifyRejectsUnknown(t *testing.T) {
	r := newTokens(t)
	unknown, _, _ := auth.NewToken(auth.APIPrefix)
	if _, err := r.tk.Verify(context.Background(), unknown); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyRejectsWrongPrefix(t *testing.T) {
	r := newTokens(t)
	tok := r.create(t, "ci", "admin")
	wrong := "cdd_" + tok[len(auth.APIPrefix):]
	if _, err := r.tk.Verify(context.Background(), wrong); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

// names are unique and short tokens of lowercase letters, digits, '.', '_'
// and '-'; a token holds known scopes only, at least one; nothing is stored
// for a refusal
func TestCreateRefuses(t *testing.T) {
	ctx := context.Background()
	r := newTokens(t)
	r.create(t, "ci", "admin")
	for _, tc := range []struct {
		name   string
		scopes []string
		err    error
	}{
		{"ci", []string{"admin"}, auth.ErrTokenNameTaken},
		{"", []string{"admin"}, auth.ErrTokenName},
		{"CI", []string{"admin"}, auth.ErrTokenName},
		{"-ci", []string{"admin"}, auth.ErrTokenName},
		{"c i", []string{"admin"}, auth.ErrTokenName},
		{strings.Repeat("a", 65), []string{"admin"}, auth.ErrTokenName},
		{"ha", []string{"root"}, auth.ErrScopeInvalid},
		{"ha", nil, auth.ErrScopeInvalid},
		{"ha", []string{"ha", "Admin"}, auth.ErrScopeInvalid},
	} {
		if tok, _, err := r.tk.Create(ctx, tc.name, tc.scopes, nil); !errors.Is(err, tc.err) || tok != "" {
			t.Errorf("%q %v: %q %v, want %v", tc.name, tc.scopes, tok, err, tc.err)
		}
	}
	for _, ok := range []string{"a", "0.ha_b-c", strings.Repeat("a", 64)} {
		r.create(t, ok, "ha")
	}
	var n int
	if err := r.pool.QueryRow(ctx, "SELECT count(*) FROM api_tokens").Scan(&n); err != nil || n != 4 {
		t.Fatalf("%d tokens, %v", n, err)
	}
}

// a list never carries a token or its hash; last use shows once a token was used
func TestListAndDelete(t *testing.T) {
	ctx := context.Background()
	r := newTokens(t)
	created := r.c.Now()
	ha := r.create(t, "ha", "ha")
	r.c.Add(time.Minute)
	r.create(t, "admin", "admin")
	r.c.Add(time.Minute)
	if _, err := r.tk.Verify(ctx, ha); err != nil {
		t.Fatal(err)
	}
	list, err := r.tk.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %v", list, err)
	}
	a, h := list[0], list[1]
	if a.Name != "admin" || !slices.Equal(a.Scopes, auth.Scopes{auth.ScopeAdmin}) || !a.CreatedAt.Equal(created.Add(time.Minute)) || a.LastUsedAt != nil {
		t.Fatalf("admin %+v", a)
	}
	if h.Name != "ha" || !slices.Equal(h.Scopes, auth.Scopes{auth.ScopeHA}) || !h.CreatedAt.Equal(created) ||
		h.LastUsedAt == nil || !h.LastUsedAt.Equal(created.Add(2*time.Minute)) {
		t.Fatalf("ha %+v", h)
	}
	if err := r.tk.Delete(ctx, "ha"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.tk.Verify(ctx, ha); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("deleted token verifies: %v", err)
	}
	if err := r.tk.Delete(ctx, "ha"); !errors.Is(err, auth.ErrTokenNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if list, _ := r.tk.List(ctx); len(list) != 1 || list[0].Name != "admin" {
		t.Fatalf("after delete %+v", list)
	}
}

// a token's use is written at most once a minute, not on every request
func TestVerifyTouchesAtMostOnceAMinute(t *testing.T) {
	ctx := context.Background()
	r := newTokens(t)
	tok := r.create(t, "ci", "admin")
	start := r.c.Now()
	lastUsed := func() time.Time {
		t.Helper()
		list, err := r.tk.List(ctx)
		if err != nil || list[0].LastUsedAt == nil {
			t.Fatalf("%+v %v", list, err)
		}
		return *list[0].LastUsedAt
	}
	for _, step := range []struct {
		advance time.Duration
		want    time.Time
	}{
		{0, start},
		{30 * time.Second, start},
		{29 * time.Second, start},
		{time.Second, start.Add(time.Minute)},
		{59 * time.Second, start.Add(time.Minute)},
		{2 * time.Minute, start.Add(time.Minute + 59*time.Second + 2*time.Minute)},
	} {
		r.c.Add(step.advance)
		if _, err := r.tk.Verify(ctx, tok); err != nil {
			t.Fatal(err)
		}
		if got := lastUsed(); !got.Equal(step.want) {
			t.Fatalf("at +%v: last used %v, want %v", r.c.Now().Sub(start), got, step.want)
		}
	}
}

// tokens made before scopes become admin tokens, and a name used twice keeps
// both tokens under unique names; down keeps admin tokens only, never
// widening a scoped one
func TestScopesMigration(t *testing.T) {
	ctx := context.Background()
	r := newTokens(t)
	raw, err := os.ReadFile("../store/migrations/00022_token_scopes.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, _ := strings.Cut(strings.SplitN(string(raw), "-- +goose Up", 2)[1], "-- +goose Down")
	r.create(t, "ha", "ha")
	r.create(t, "both", "admin", "ha")
	if _, err := r.pool.Exec(ctx, down); err != nil {
		t.Fatal(err)
	}
	var left []string
	if err := r.pool.QueryRow(ctx, "SELECT coalesce(array_agg(name), '{}') FROM api_tokens").Scan(&left); err != nil ||
		!slices.Equal(left, []string{"both"}) {
		t.Fatalf("down kept %v %v", left, err)
	}
	names := []string{"ci", "ci", "owner"}
	var old []string
	for i, name := range names {
		tok, hash, _ := auth.NewToken(auth.APIPrefix)
		old = append(old, tok)
		if _, err := r.pool.Exec(ctx, "INSERT INTO api_tokens (name, hash, created_at) VALUES ($1, $2, $3)",
			name, hash, r.c.Now().Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		if got, err := r.tk.Verify(ctx, old[i]); err != nil || !slices.Equal(got.Scopes, auth.Scopes{auth.ScopeAdmin}) {
			t.Fatalf("old token %d: %v %v", i, got, err)
		}
		var stored string
		if err := r.pool.QueryRow(ctx, "SELECT name FROM api_tokens WHERE hash = $1", auth.Hash(old[i])).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		// the later of the two keeps its name with the id's start appended
		if (i == 1) == (stored == name) || !strings.HasPrefix(stored, name) {
			t.Fatalf("token %d is named %q", i, stored)
		}
	}
	if _, err := r.pool.Exec(ctx, "INSERT INTO api_tokens (name, hash, scopes) VALUES ('x', 'h', '{root}')"); err == nil {
		t.Fatal("an unknown scope was stored")
	}
	if _, err := r.pool.Exec(ctx, "INSERT INTO api_tokens (name, hash, scopes) VALUES ('y', 'i', '{}')"); err == nil {
		t.Fatal("a token without a scope was stored")
	}
}
