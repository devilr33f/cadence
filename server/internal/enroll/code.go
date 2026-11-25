package enroll

import (
	"crypto/rand"
	"strings"
)

const (
	alphabet   = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	codeLength = 8
)

// NewCode returns a random code shown as XXXX-XXXX.
func NewCode() (display string, err error) {
	b := make([]byte, codeLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)] // 256 is a multiple of 32: no bias
	}
	return string(b[:4]) + "-" + string(b[4:]), nil
}

// NormalizeCode maps typed input to the stored form.
func NormalizeCode(in string) (string, bool) {
	var sb strings.Builder
	for _, r := range strings.ToUpper(in) {
		switch r {
		case '-', ' ':
			continue
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		}
		sb.WriteRune(r)
	}
	s := sb.String()
	if len(s) != codeLength {
		return "", false
	}
	for _, r := range s {
		if !strings.ContainsRune(alphabet, r) {
			return "", false
		}
	}
	return s, true
}
