// Package secrets seals provider keys for storage in the database.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"runtime"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/config"
)

const (
	version = 1
	keyLen  = 32
	kidLen  = 8
	envName = "CADENCE_MASTER_KEY_FILE"
)

var errOpen = errors.New("secrets: cannot open sealed value")

// Box seals with AES-256-GCM. Layout: version, len(kid), kid, nonce, ciphertext+tag.
type Box struct {
	aead cipher.AEAD
	kid  []byte
}

func New(key []byte) (*Box, error) {
	if len(key) != keyLen {
		return nil, errors.New("secrets: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(key)
	return &Box{aead: aead, kid: []byte(hex.EncodeToString(sum[:])[:kidLen])}, nil
}

// Load errors name the variable only, never the path or the content.
func Load(path string) (*Box, error) {
	fail := func(reason string) error { return config.Error{Variable: envName, Reason: reason} }
	info, err := os.Stat(path)
	if err != nil {
		return nil, fail("unreadable")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fail("must not be readable by group or others")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the path is operator config
	if err != nil {
		return nil, fail("unreadable")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != keyLen {
		return nil, fail("must hold base64 of 32 bytes")
	}
	return New(key)
}

func (b *Box) Seal(plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 2+len(b.kid)+len(nonce)+len(plain)+b.aead.Overhead())
	out = append(out, version, kidLen)
	out = append(out, b.kid...)
	out = append(out, nonce...)
	return b.aead.Seal(out, nonce, plain, aad), nil
}

func (b *Box) Open(sealed, aad []byte) ([]byte, error) {
	if len(sealed) < 2 || sealed[0] != version {
		return nil, errOpen
	}
	head := 2 + int(sealed[1])
	n := b.aead.NonceSize()
	if len(sealed) < head+n+b.aead.Overhead() {
		return nil, errOpen
	}
	plain, err := b.aead.Open(nil, sealed[head:head+n], sealed[head+n:], aad)
	if err != nil {
		return nil, errOpen
	}
	return plain, nil
}
