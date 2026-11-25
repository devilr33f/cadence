package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id's costs: memory in KiB, passes and lanes.
type Params struct {
	Memory  uint32
	Time    uint32
	Threads uint8
}

// DefaultParams hash every new password (M7a-D4): 64 MiB, 3 passes, 2 lanes.
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 2}

const (
	saltLen = 16
	keyLen  = 32
	// a stored hash asking for more is refused rather than run (256 MiB,
	// 10 passes, 16 lanes)
	maxMemory  = 256 * 1024
	maxTime    = 10
	maxThreads = 16
)

// ErrBadHash is a stored hash this package cannot read.
var ErrBadHash = errors.New("auth: bad password hash")

var b64 = base64.RawStdEncoding

// dummyHash is checked when no password is set, so a login costs the same
// argon2 work either way; its key has no known password and its result is
// never trusted.
var dummyHash = encodePHC(DefaultParams, make([]byte, saltLen), make([]byte, keyLen))

// HashPassword hashes with the default params as a PHC string.
func HashPassword(password string) (string, error) { return DefaultParams.Hash(password) }

// Hash is the PHC string $argon2id$v=19$m=…,t=…,p=…$<salt>$<key> of password
// under a fresh 16-byte salt.
func (p Params) Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return encodePHC(p, salt, argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, keyLen)), nil
}

func encodePHC(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key))
}

// VerifyPassword reports whether password matches the PHC string, under the
// params the string names; the keys are compared in constant time.
func VerifyPassword(encoded, password string) (bool, error) {
	p, salt, key, err := decodePHC(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(key))) //nolint:gosec // decodePHC bounds the key to 64 bytes
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

func decodePHC(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Params{}, nil, nil, ErrBadHash
	}
	p, ok := decodeParams(parts[3])
	if !ok {
		return Params{}, nil, nil, ErrBadHash
	}
	salt, serr := b64.Strict().DecodeString(parts[4])
	key, kerr := b64.Strict().DecodeString(parts[5])
	if serr != nil || kerr != nil || len(salt) < 8 || len(salt) > 64 || len(key) < 16 || len(key) > 64 {
		return Params{}, nil, nil, ErrBadHash
	}
	return p, salt, key, nil
}

// decodeParams reads exactly "m=<n>,t=<n>,p=<n>" within argon2's and this
// server's bounds.
func decodeParams(s string) (Params, bool) {
	fields := strings.Split(s, ",")
	if len(fields) != 3 {
		return Params{}, false
	}
	var v [3]uint64
	for i, name := range []string{"m=", "t=", "p="} {
		num, ok := strings.CutPrefix(fields[i], name)
		n, err := strconv.ParseUint(num, 10, 32)
		if !ok || err != nil {
			return Params{}, false
		}
		v[i] = n
	}
	m, t, p := v[0], v[1], v[2]
	if t < 1 || t > maxTime || p < 1 || p > maxThreads || m < 8*p || m > maxMemory {
		return Params{}, false
	}
	return Params{Memory: uint32(m), Time: uint32(t), Threads: uint8(p)}, true
}
