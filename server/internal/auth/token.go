// Package auth issues and verifies bearer tokens.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

const (
	APIPrefix    = "cdn_"
	DevicePrefix = "cdd_"
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken returns the token and its sha256; only the hash is stored.
func NewToken(prefix string) (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = prefix + strings.ToLower(encoding.EncodeToString(b))
	return token, Hash(token), nil
}

func Hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
