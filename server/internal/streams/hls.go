package streams

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"net/url"
	"strings"
)

// rewritePlaylist names every uri of an HLS playlist (a line of its own, or a tag's URI="…") by what child answers
// for it, resolved against base; it answers the playlist, how many uris it rewrote, and false when child refused
// one (a host off the list), so no upstream url ever reaches the device
func rewritePlaylist(src string, base *url.URL, child func(*url.URL) (string, bool)) (string, int, bool) {
	var b strings.Builder
	n := 0
	ok := true
	resolve := func(ref string) (string, bool) {
		u, err := base.Parse(strings.TrimSpace(ref))
		if err != nil {
			return "", false
		}
		n++
		return child(u)
	}
	for line := range strings.Lines(src) {
		body := strings.TrimRight(line, "\r\n")
		end := line[len(body):]
		switch {
		case strings.TrimSpace(body) == "":
		case strings.HasPrefix(body, "#"):
			body = rewriteURIAttr(body, func(ref string) string {
				s, good := resolve(ref)
				ok = ok && good
				return s
			})
		default:
			s, good := resolve(body)
			ok = ok && good
			body = s
		}
		b.WriteString(body)
		b.WriteString(end)
	}
	return b.String(), n, ok
}

// rewriteURIAttr replaces the value of each URI="…" attribute of a tag line
func rewriteURIAttr(line string, f func(string) string) string {
	const key = `URI="`
	var b strings.Builder
	for {
		i := strings.Index(line, key)
		if i < 0 {
			b.WriteString(line)
			return b.String()
		}
		rest := line[i+len(key):]
		j := strings.IndexByte(rest, '"')
		if j < 0 {
			b.WriteString(line)
			return b.String()
		}
		b.WriteString(line[:i+len(key)])
		b.WriteString(f(rest[:j]))
		b.WriteByte('"')
		line = rest[j+1:]
	}
}

// sealer hides an upstream url inside a relay path: AES-GCM under a key the process draws at start, bound to the
// token it was issued for, so a sealed url works only while its token lives and tells the device nothing
type sealer struct{ aead cipher.AEAD }

func newSealer() *sealer {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("streams: no randomness for the relay's key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic("streams: " + err.Error())
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic("streams: " + err.Error())
	}
	return &sealer{aead: aead}
}

func (s *sealer) seal(token, raw string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, []byte(raw), []byte(token))
	return base64.RawURLEncoding.EncodeToString(out), nil
}

func (s *sealer) open(token, sealed string) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(b) < s.aead.NonceSize() {
		return "", false
	}
	ns := s.aead.NonceSize()
	raw, err := s.aead.Open(nil, b[:ns], b[ns:], []byte(token))
	if err != nil {
		return "", false
	}
	return string(raw), true
}
