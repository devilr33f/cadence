package vkmusic

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Module takes the secret token: an access token of VK's official Android
// client (client_id 2274003) with the scope audio,offline; Kate Mobile's
// no longer reaches audio. config {userAgent} is the user agent the token was
// issued with (DefaultUserAgent when unset): vk refuses audio to a call with
// another. The host is fixed so the token only goes to VK.
//
// The server never logs in with a password. The owner gets a token once, on
// their own machine, with vkpymusic's TokenReceiver, or by opening
//
//	https://oauth.vk.com/token?grant_type=password&client_id=2274003&client_secret=hHbZxrka2uZ6jB1inYsH&username=<login>&password=<password>&scope=audio,offline&2fa_supported=1&v=5.131
//
// with that user agent (curl -A); a second factor's code goes in &code=, and a
// captcha needs a browser. The answer's access_token is the secret.
var Module = providers.Module{
	ID:   "vk-music",
	Kind: providers.KindMusic,
	ConfigSchema: `{"type":"object","additionalProperties":false,"properties":{` +
		`"userAgent":{"type":"string","minLength":1,"maxLength":300}}}`,
	Secrets: []string{"token"},
	Probe:   probe,
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			UserAgent string `json:"userAgent"`
		}
		if len(cfg) > 0 && json.Unmarshal(cfg, &c) != nil {
			return nil, errors.New("vkmusic: bad config")
		}
		tok := sec["token"]
		if tok == "" || strings.ContainsAny(tok, " \t\r\n") {
			return nil, errors.New("vkmusic: token missing")
		}
		if strings.ContainsAny(c.UserAgent, "\r\n") {
			return nil, errors.New("vkmusic: bad user agent")
		}
		return New(Config{Token: tok, UserAgent: c.UserAgent}, httpx.Client(nil)), nil
	},
}

// probe runs one search of one song, which checks the token's audio access
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	var a itemsAnswer
	err := c.call(ctx, "audio.search", url.Values{"q": {"test"}, "count": {"1"}}, &a)
	var status statusError
	var api apiError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &status):
		return providers.ProbeStatus(int(status))
	case errors.As(err, &api):
		if api.signIn() {
			return providers.ProbeAuth
		}
		if api.rateLimited() {
			return providers.ProbeRateLimited
		}
		return providers.ProbeBadResponse
	case errors.Is(err, errBadAnswer), errors.Is(err, httpx.ErrRedirect):
		return providers.ProbeBadResponse
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return providers.ProbeTimeout
	}
	return providers.ProbeUnreachable
}
