package auth_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

func TestPasswordHashIsPHC(t *testing.T) {
	enc, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	phc := regexp.MustCompile(`^\$argon2id\$v=19\$m=65536,t=3,p=2\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)
	if !phc.MatchString(enc) {
		t.Fatalf("not the PHC form: %d chars", len(enc))
	}
	if strings.Contains(enc, "correct horse") {
		t.Fatal("hash carries the password")
	}
	again, _ := auth.HashPassword("correct horse battery")
	if again == enc {
		t.Fatal("same salt twice")
	}
	for pw, want := range map[string]bool{"correct horse battery": true, "correct horse batterY": false, "": false} {
		ok, err := auth.VerifyPassword(enc, pw)
		if err != nil || ok != want {
			t.Fatalf("%q: %v %v", pw, ok, err)
		}
	}
}

// the params are read from the stored string, so a later change of the
// defaults keeps old hashes working
func TestPasswordVerifiesOtherParams(t *testing.T) {
	enc, err := auth.Params{Memory: 8 * 1024, Time: 1, Threads: 1}.Hash("another password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatalf("prefix of %q", enc[:31])
	}
	if ok, err := auth.VerifyPassword(enc, "another password"); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if ok, err := auth.VerifyPassword(enc, "another passwore"); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestPasswordRefusesBadEncodings(t *testing.T) {
	salt, key := "AAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, enc := range []string{
		"",
		"plain",
		"$argon2i$v=19$m=65536,t=3,p=2$" + salt + "$" + key,
		"$argon2id$v=16$m=65536,t=3,p=2$" + salt + "$" + key,
		"$argon2id$v=19$t=3,m=65536,p=2$" + salt + "$" + key,
		"$argon2id$v=19$m=65536,t=3,p=2,x=1$" + salt + "$" + key,
		"$argon2id$v=19$m=65536,t=0,p=2$" + salt + "$" + key,
		"$argon2id$v=19$m=65536,t=3,p=0$" + salt + "$" + key,
		"$argon2id$v=19$m=8,t=3,p=2$" + salt + "$" + key,
		"$argon2id$v=19$m=99999999,t=3,p=2$" + salt + "$" + key,
		"$argon2id$v=19$m=262145,t=3,p=2$" + salt + "$" + key,
		"$argon2id$v=19$m=65536,t=11,p=2$" + salt + "$" + key,
		"$argon2id$v=19$m=65536,t=3,p=17$" + salt + "$" + key,
		"$argon2id$v=19$m=65536,t=3,p=2$" + salt + "==$" + key,
		"$argon2id$v=19$m=65536,t=3,p=2$" + salt + "$" + key + "$",
		"$argon2id$v=19$m=65536,t=3,p=2$" + salt + "$AAAA",
		"$argon2id$v=19$m=65536,t=3,p=2$AAAA$" + key,
	} {
		if _, err := auth.VerifyPassword(enc, "x"); !errors.Is(err, auth.ErrBadHash) {
			t.Errorf("%q: err %v", enc, err)
		}
	}
}

// stored params are bounded, so a hash written by hand cannot make a login run
// for minutes or take the server's memory
func TestPasswordParamBounds(t *testing.T) {
	for s, ok := range map[string]bool{
		"m=262144,t=10,p=16": true,
		"m=128,t=1,p=16":     true,
		"m=262145,t=3,p=2":   false,
		"m=65536,t=11,p=2":   false,
		"m=65536,t=3,p=17":   false,
		"m=127,t=1,p=16":     false,
		"m=65536,t=3,p=2,":   false,
		"m=65536,t=+3,p=2":   false,
	} {
		if _, got := auth.DecodeParams(s); got != ok {
			t.Errorf("%s: %v, want %v", s, got, ok)
		}
	}
}
