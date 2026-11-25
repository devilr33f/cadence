package yandexmusic

import (
	"context"
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Module needs no secret: search, lists and radio are anonymous, and songs
// then play from YouTube Music. Optional secrets: token (an account's OAuth
// music token) and cookies (a browser's Cookie header or Netscape
// cookies.txt holding yandex.ru's Session_id, exchanged for a token); a token
// wins over cookies. With a Plus subscription either plays songs whole. My
// Wave's batches need one of them too. The hosts are fixed so the secrets only
// go to Yandex.
var Module = providers.Module{
	ID:   "yandex-music",
	Kind: providers.KindMusic,
	ConfigSchema: `{"type":"object","additionalProperties":false,` +
		`"properties":{"quality":{"enum":["hq","nq","lossless"]}}}`,
	OptionalSecrets: []string{"token", "cookies"},
	Probe:           probe,
	New: func(raw json.RawMessage, sec map[string]string) (any, error) {
		var cfg struct {
			Quality string `json:"quality"`
		}
		if len(raw) > 0 && json.Unmarshal(raw, &cfg) != nil {
			return nil, errors.New("yandexmusic: bad config")
		}
		if _, ok := codecs[cfg.Quality]; !ok && cfg.Quality != "" {
			return nil, errors.New("yandexmusic: bad quality")
		}
		c := Config{Quality: cfg.Quality}
		switch {
		case sec["token"] != "":
			if !ValidToken(sec["token"]) {
				return nil, errors.New("yandexmusic: bad token")
			}
			c.Token = sec["token"]
		case sec["cookies"] != "":
			cookies, ok := ParseCookies(sec["cookies"])
			if !ok {
				return nil, errors.New("yandexmusic: cookies without Session_id")
			}
			c.Cookies = cookies
		}
		return New(c, httpx.Client(nil)), nil
	},
}

// probe checks the account's token (exchanged from the cookies first) with
// the account status, or runs one anonymous search without one; both cost
// nothing
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	var r json.RawMessage
	var err error
	if c.cfg.Token != "" || len(c.cfg.Cookies) > 0 {
		err = c.get(ctx, "/account/status", nil, true, &r)
	} else {
		err = c.get(ctx, "/search", searchQuery("test", "track"), false, &r)
	}
	var status statusError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &status):
		return providers.ProbeStatus(int(status))
	case errors.Is(err, errBadAnswer), errors.Is(err, httpx.ErrRedirect):
		return providers.ProbeBadResponse
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return providers.ProbeTimeout
	}
	return providers.ProbeUnreachable
}
