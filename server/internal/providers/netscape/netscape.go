// Package netscape reads a cookies secret: a Netscape cookies.txt as a browser
// extension exports it, as it is or in base64 (the one-line form a
// single-line field keeps). Errors never quote the secret.
package netscape

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

const (
	// MaxBytes bounds the cookies secret, as decoded
	MaxBytes = 512 << 10
	header   = "# Netscape HTTP Cookie File"
	// a cookie line's fields: domain, subdomains, path, secure, expiry, name, value
	fields = 7
)

var ErrBad = errors.New("cookies are not a Netscape cookies.txt")

// Cookie is one line of the file.
type Cookie struct {
	Domain, Path, Name, Value string
}

// Parse answers the file with its header, nil for an empty secret, or ErrBad.
func Parse(secret string) ([]byte, error) {
	s := strings.TrimSpace(secret)
	if s == "" {
		return nil, nil
	}
	raw := []byte(s)
	if !strings.Contains(s, "\t") {
		dec, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
		if err != nil {
			return nil, ErrBad
		}
		raw = dec
	}
	if len(raw) > MaxBytes {
		return nil, ErrBad
	}
	raw = append(bytes.TrimSpace(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))), '\n')
	if len(Cookies(raw)) == 0 || !wellFormed(raw) {
		return nil, ErrBad
	}
	// yt-dlp reads a file without the header only with a warning
	if !bytes.HasPrefix(raw, []byte(header)) {
		raw = append([]byte(header+"\n"), raw...)
	}
	return raw, nil
}

func wellFormed(file []byte) bool {
	for line := range strings.SplitSeq(string(file), "\n") {
		if cookieLine(line) && len(strings.Split(strings.TrimSpace(line), "\t")) != fields {
			return false
		}
	}
	return true
}

// #HttpOnly_ marks a cookie line, not a comment
func cookieLine(line string) bool {
	line = strings.TrimSpace(line)
	return line != "" && (!strings.HasPrefix(line, "#") || strings.HasPrefix(line, "#HttpOnly_"))
}

// Cookies answers every well-formed cookie of a file Parse answered, in order.
func Cookies(file []byte) []Cookie {
	var out []Cookie
	for line := range strings.SplitSeq(string(file), "\n") {
		if !cookieLine(line) {
			continue
		}
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) != fields {
			continue
		}
		out = append(out, Cookie{Domain: strings.TrimPrefix(f[0], "#HttpOnly_"), Path: f[2], Name: f[5], Value: f[6]})
	}
	return out
}

// For answers the cookies sent to host (a domain cookie ".x.com" or "x.com"
// matches it and its subdomains), the last of a repeated name winning.
func For(file []byte, host string) map[string]string {
	out := map[string]string{}
	for _, c := range Cookies(file) {
		d := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		if host == d || strings.HasSuffix(host, "."+d) {
			out[c.Name] = c.Value
		}
	}
	return out
}

// Set answers file with the cookie name of domain set to value: the line of that name whose domain is domain (with
// or without its leading dot) gets the value and expires (unix seconds, 0 for a session cookie), else a line for
// ".domain" is added. The file's other lines stay as they are.
func Set(file []byte, domain, name, value string, expires int64) []byte {
	bare := strings.TrimPrefix(strings.ToLower(domain), ".")
	lines := strings.Split(strings.TrimRight(string(file), "\n"), "\n")
	exp := strconv.FormatInt(max(expires, 0), 10)
	for i, line := range lines {
		if !cookieLine(line) {
			continue
		}
		f := strings.Split(strings.TrimSpace(line), "\t")
		d := strings.TrimPrefix(strings.ToLower(strings.TrimPrefix(f[0], "#HttpOnly_")), ".")
		if len(f) == fields && d == bare && f[5] == name {
			f[4], f[6] = exp, value
			lines[i] = strings.Join(f, "\t")
			return []byte(strings.Join(lines, "\n") + "\n")
		}
	}
	lines = append(lines, strings.Join([]string{"." + bare, "TRUE", "/", "TRUE", exp, name, value}, "\t"))
	return []byte(strings.Join(lines, "\n") + "\n")
}
