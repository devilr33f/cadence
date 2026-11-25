package xweb

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/netscape"
)

// ErrBadCookies refuses a cookies secret that is not a Netscape cookies.txt
var ErrBadCookies = errors.New("xweb: cookies are not a Netscape cookies.txt")

// Module is X search as the account whose cookies it holds: its one secret,
// cookies, is a Netscape cookies.txt of a signed-in x.com session (as yt-dlp's
// is), as it is or in base64; it needs auth_token and ct0. The hosts are
// fixed, so the cookies only go to X; a cookie X renews is saved back to it.
func Module(log *slog.Logger) providers.Module {
	return providers.Module{
		ID:           "x-web",
		Kind:         providers.KindXSearch,
		ConfigSchema: `{"type":"object","additionalProperties":false}`,
		Secrets:      []string{"cookies"},
		Probe:        probe,
		NewSaving: func(_ json.RawMessage, sec map[string]string, save providers.SaveSecret) (any, error) {
			file, err := netscape.Parse(sec["cookies"])
			if err != nil || file == nil {
				return nil, ErrBadCookies
			}
			return New(Config{Cookies: file, Save: save, Log: log}, httpx.Client(nil))
		},
	}
}

// probe reads the web app as the account (its home page and scripts), which
// spends no search: it says the cookies still sign in and the page still has
// what a search needs
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	_, err := c.load(ctx)
	var f *failure
	switch {
	case err == nil:
		return nil
	case errors.As(err, &f) && f.code == "timeout":
		return providers.ProbeTimeout
	case errors.As(err, &f) && f.code == "send":
		return providers.ProbeUnreachable
	case errors.As(err, &f) && f.code == "signed_out":
		return providers.ProbeAuth
	}
	return providers.ProbeBadResponse
}
