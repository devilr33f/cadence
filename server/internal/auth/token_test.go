package auth_test

import (
	"bytes"
	"regexp"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

func TestNewTokenFormat(t *testing.T) {
	tok, hash, err := auth.NewToken(auth.APIPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^cdn_[a-z2-7]{52}$`).MatchString(tok) {
		t.Fatalf("format %d chars", len(tok))
	}
	if !bytes.Equal(hash, auth.Hash(tok)) {
		t.Fatal("hash mismatch")
	}
}

func TestHashStable(t *testing.T) {
	if !bytes.Equal(auth.Hash("x"), auth.Hash("x")) || bytes.Equal(auth.Hash("x"), auth.Hash("y")) {
		t.Fatal("hash not stable or collides")
	}
	if len(auth.Hash("x")) != 32 {
		t.Fatal("not sha256")
	}
}
