package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

func TestOwnerPasswordSetAndCheck(t *testing.T) {
	ctx := context.Background()
	o := auth.NewOwner(storetest.Pool(t), time.Now)
	if _, err := o.SetPassword(ctx, "elevenchars"); !errors.Is(err, auth.ErrPasswordShort) {
		t.Fatalf("11 characters: %v", err)
	}
	// characters, not bytes
	if _, err := o.SetPassword(ctx, "пароль-абвг"); !errors.Is(err, auth.ErrPasswordShort) {
		t.Fatalf("11 cyrillic characters: %v", err)
	}
	if _, err := o.SetPassword(ctx, "twelve chars"); err != nil {
		t.Fatal(err)
	}
	for pw, want := range map[string]bool{"twelve chars": true, "twelve charz": false, "": false} {
		if ok, at, err := o.Check(ctx, pw); err != nil || ok != want || at.IsZero() {
			t.Fatalf("%q: %v %v %v", pw, ok, at, err)
		}
	}
	_, before, _ := o.Check(ctx, "twelve chars")
	if _, err := o.SetPassword(ctx, "a newer password"); err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := o.Check(ctx, "twelve chars"); ok {
		t.Fatal("old password still checks")
	}
	if ok, after, _ := o.Check(ctx, "a newer password"); !ok || !after.After(before) {
		t.Fatalf("new password: %v, version %v after %v", ok, after, before)
	}
}

// a password change ends every session and forgets every known browser
func TestOwnerPasswordRevokesSessionsAndBrowsers(t *testing.T) {
	ctx := context.Background()
	r := newStore(t)
	a, b := r.open(t, ""), r.open(t, "")
	revoked, err := auth.NewOwner(r.pool, time.Now).SetPassword(ctx, "twelve chars")
	if err != nil || revoked != (auth.Revoked{Sessions: 2, Browsers: 2}) {
		t.Fatalf("revoked %+v %v", revoked, err)
	}
	for _, g := range []auth.Grant{a, b} {
		if r.valid(t, g.Session.Token) || r.known(t, g.Browser) {
			t.Fatal("a session or browser survived")
		}
	}
}

// with no password set a check does the same argon2 work, against a fixed
// hash of the default params, and never succeeds
func TestOwnerCheckWithoutPasswordTakesTheSamePath(t *testing.T) {
	ctx := context.Background()
	o := auth.NewOwner(storetest.Pool(t), time.Now)
	var seen []string
	auth.SetVerify(o, func(encoded, _ string) (bool, error) {
		seen = append(seen, encoded)
		return true, nil
	})
	if ok, _, err := o.Check(ctx, "anything at all"); err != nil || ok {
		t.Fatalf("no password: %v %v", ok, err)
	}
	if len(seen) != 1 || seen[0] != auth.DummyHash {
		t.Fatalf("verified %q", seen)
	}
	want, _ := auth.HashPassword("x")
	if auth.DummyHash[:31] != want[:31] {
		t.Fatalf("dummy params %q, defaults %q", auth.DummyHash[:31], want[:31])
	}
	if ok, err := auth.VerifyPassword(auth.DummyHash, "anything at all"); err != nil || ok {
		t.Fatalf("dummy verifies: %v %v", ok, err)
	}

	seen = nil
	auth.SetVerify(o, auth.VerifyPassword)
	if _, err := o.SetPassword(ctx, "twelve chars"); err != nil {
		t.Fatal(err)
	}
	auth.SetVerify(o, func(encoded, password string) (bool, error) {
		seen = append(seen, encoded)
		return auth.VerifyPassword(encoded, password)
	})
	if ok, _, _ := o.Check(ctx, "wrong password"); ok || len(seen) != 1 || seen[0] == auth.DummyHash {
		t.Fatalf("set password: %v %q", ok, seen)
	}
}

// a stored hash past the bounds is not run: the check fails closed
func TestOwnerCheckRefusesOversizedParams(t *testing.T) {
	ctx := context.Background()
	pool := storetest.Pool(t)
	hash := "$argon2id$v=19$m=262145,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := pool.Exec(ctx, "INSERT INTO admin_users VALUES (1, $1, now())", hash); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := auth.NewOwner(pool, time.Now).Check(ctx, "twelve chars"); ok || !errors.Is(err, auth.ErrBadHash) {
		t.Fatalf("%v %v", ok, err)
	}
}
