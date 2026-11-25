package ytdlp

import (
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers/netscape"
)

// MaxCookiesBytes bounds the cookies secret, as decoded
const MaxCookiesBytes = netscape.MaxBytes

var ErrBadCookies = errors.New("ytdlp: cookies are not a Netscape cookies.txt")

// Cookies reads the cookies secret: a Netscape cookies.txt as it is, or that
// file in base64 (a one-line form a single-line field keeps). It answers the
// file with its header, nil for an empty secret, or ErrBadCookies; the error
// never quotes the secret.
func Cookies(secret string) ([]byte, error) {
	b, err := netscape.Parse(secret)
	if err != nil {
		return nil, ErrBadCookies
	}
	return b, nil
}
