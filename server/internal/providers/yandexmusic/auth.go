package yandexmusic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers/netscape"
)

// the public client ids of Yandex's mobile apps, which the session exchange
// takes: not the owner's secrets
const (
	sessionClientID     = "c0ebe342af7d48fbbbfcf2d2eedb8f9e"
	sessionClientSecret = "ad0a908f0aa341a182a37ecd75bc319e"
	musicClientID       = "23cabbbdc6cd418abb4b39c32c41195d"
	musicClientSecret   = "53bc75238f0c4d08a118e51fe9203300" //nolint:gosec // the music app's public client secret
	authUserAgent       = "com.yandex.mobile.auth.sdk/7.33.2.733022870"
	passportHost        = "passport.yandex.ru"
	// an exchanged token is exchanged again after this long, or at once when the API refuses it
	tokenTTL = 12 * time.Hour
)

// errNoAccount is a stream asked of an instance with no token and no cookies
var errNoAccount = errors.New("yandexmusic: no account")

var (
	tokenRe       = regexp.MustCompile(`^[0-9A-Za-z_.\-]{16,512}$`)
	cookieValueRe = regexp.MustCompile(`^[0-9A-Za-z_.:|\-%*]{1,1000}$`)
)

// the session cookies the exchange sends, in its order
var sessionCookies = []string{"Session_id", "sessionid2"}

// ValidToken says whether a token secret is one an Authorization header can carry.
func ValidToken(s string) bool { return tokenRe.MatchString(s) }

// ParseCookies reads the cookies secret: a browser's Cookie header
// ("a=b; c=d") or a Netscape cookies.txt (as it is or in base64). It answers
// the session cookies alone, or ok false without Session_id.
func ParseCookies(secret string) (map[string]string, bool) {
	all := map[string]string{}
	if file, err := netscape.Parse(secret); err == nil && file != nil {
		all = netscape.For(file, passportHost)
	} else {
		for part := range strings.SplitSeq(secret, ";") {
			name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
			if ok {
				all[strings.TrimSpace(name)] = strings.TrimSpace(value)
			}
		}
	}
	out := map[string]string{}
	for _, name := range sessionCookies {
		if v := all[name]; cookieValueRe.MatchString(v) {
			out[name] = v
		}
	}
	return out, out["Session_id"] != ""
}

// accessToken answers the token a call sends and whether it was exchanged
// (only an exchanged one is worth exchanging again on a refusal)
func (c *Client) accessToken(ctx context.Context, refresh bool) (string, bool, error) {
	if c.cfg.Token != "" {
		return c.cfg.Token, false, nil
	}
	if len(c.cfg.Cookies) == 0 {
		return "", false, errNoAccount
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !refresh && c.token != "" && c.cfg.Now().Sub(c.exchangedAt) < tokenTTL {
		return c.token, true, nil
	}
	c.token = ""
	tok, err := c.exchange(ctx)
	if err != nil {
		return "", true, err
	}
	c.token, c.exchangedAt = tok, c.cfg.Now()
	return tok, true, nil
}

// exchange turns the session cookies into a passport x-token, then that into
// a music token, as Yandex's mobile sign-in does
func (c *Client) exchange(ctx context.Context) (string, error) {
	var parts []string
	for _, name := range sessionCookies {
		if v := c.cfg.Cookies[name]; v != "" {
			parts = append(parts, name+"="+v)
		}
	}
	xtoken, err := c.postToken(ctx, c.cfg.PassportURL+"/1/bundle/oauth/token_by_sessionid", http.Header{
		"Ya-Client-Host":   {passportHost},
		"Ya-Client-Cookie": {strings.Join(parts, "; ")},
	}, url.Values{"client_id": {sessionClientID}, "client_secret": {sessionClientSecret}})
	if err != nil {
		return "", err
	}
	return c.postToken(ctx, c.cfg.OAuthURL+"/1/token", nil, url.Values{
		"grant_type": {"x-token"}, "access_token": {xtoken},
		"client_id": {musicClientID}, "client_secret": {musicClientSecret},
	})
}

// postToken answers a token endpoint's access_token; a refusal of any kind
// but a rate limit or a server error is the cookies' (401)
func (c *Client) postToken(ctx context.Context, endpoint string, h http.Header, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", authUserAgent)
	b, err := c.do(req)
	var status statusError
	if errors.As(err, &status) && status >= 400 && status < 500 && status != http.StatusTooManyRequests {
		return "", statusError(http.StatusUnauthorized)
	}
	if err != nil {
		return "", err
	}
	var a struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(b, &a) != nil {
		return "", errBadAnswer
	}
	if !tokenRe.MatchString(a.AccessToken) {
		return "", statusError(http.StatusUnauthorized)
	}
	return a.AccessToken, nil
}
