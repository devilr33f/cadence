package soloist

import "strings"

const (
	secretKey = "apiKey"
	maxKey    = 256

	keyMissing = "missing"
	keyInvalid = "invalid"
)

// checkKey is the owner's api key as soloist takes it (reference §1.5): one
// trailing newline dropped, then 1-256 bytes of 0x21-0x7e. A key that is not
// one is "" with the reason, missing or invalid, and goes nowhere.
func checkKey(secrets map[string]string) (key, problem string) {
	raw := secrets[secretKey]
	if raw == "" {
		return "", keyMissing
	}
	if k, ok := strings.CutSuffix(raw, "\r\n"); ok {
		raw = k
	} else {
		raw = strings.TrimSuffix(raw, "\n")
	}
	if raw == "" || len(raw) > maxKey {
		return "", keyInvalid
	}
	for i := range len(raw) {
		if raw[i] < 0x21 || raw[i] > 0x7e {
			return "", keyInvalid
		}
	}
	return raw, ""
}
