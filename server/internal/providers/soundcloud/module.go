package soundcloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

var clientIDSecretRe = regexp.MustCompile(`^[0-9A-Za-z]{32}$`)

// Module needs no secret: the site's own client id is read from its scripts.
// Optional secrets: clientId (a fixed one, when the site's cannot be read)
// and oauthToken (a signed-in account's, which plays a Go+ song whole). The
// hosts are fixed so they only go to SoundCloud.
var Module = providers.Module{
	ID:              "soundcloud",
	Kind:            providers.KindMusic,
	ConfigSchema:    `{"type":"object","additionalProperties":false}`,
	OptionalSecrets: []string{"clientId", "oauthToken"},
	Probe:           probe,
	New: func(_ json.RawMessage, sec map[string]string) (any, error) {
		if id := sec["clientId"]; id != "" && !clientIDSecretRe.MatchString(id) {
			return nil, errors.New("soundcloud: bad client id")
		}
		return New(Config{ClientID: sec["clientId"], OAuthToken: sec["oauthToken"]}, httpx.Client(nil)), nil
	},
}

// probe reads the client id and runs one search, which costs nothing
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	var a struct {
		Collection []trackItem `json:"collection"`
	}
	err := c.get(ctx, "/search/tracks", url.Values{"q": {"test"}, "limit": {"1"}}, &a)
	var status statusError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &status):
		if status == 401 || status == 403 {
			return providers.ProbeAuth
		}
		return providers.ProbeStatus(int(status))
	case errors.Is(err, errBadAnswer), errors.Is(err, errNoID), errors.Is(err, httpx.ErrRedirect):
		return providers.ProbeBadResponse
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return providers.ProbeTimeout
	}
	return providers.ProbeUnreachable
}
