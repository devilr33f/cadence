package xweb

import (
	"context"
	"net/http"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers/netscape"
)

// how long a save of the renewed jar may take: it runs apart from the search
const saveTimeout = 10 * time.Second

func (c *Client) csrfToken() string {
	c.cookieMu.Lock()
	defer c.cookieMu.Unlock()
	return c.csrf
}

// absorb takes in the cookies x set on an answer of its own (ct0, the csrf token, rotates): each new value is sent
// from the next request on, and the jar is saved once it changed. A cookie x clears (expired, or empty) is left as
// it was: losing auth_token would sign the instance out for good, and x clearing it means it already has
func (c *Client) absorb(resp *http.Response) {
	now := c.cfg.Now()
	changed := false
	c.cookieMu.Lock()
	for _, k := range resp.Cookies() {
		domain := strings.TrimPrefix(strings.ToLower(k.Domain), ".")
		if domain != "" && domain != "x.com" && !strings.HasSuffix(domain, ".x.com") {
			continue
		}
		if k.Value == "" || k.MaxAge < 0 || (!k.Expires.IsZero() && k.Expires.Before(now)) || c.cookies[k.Name] == k.Value {
			continue
		}
		var expires int64
		switch {
		case k.MaxAge > 0:
			expires = now.Add(time.Duration(k.MaxAge) * time.Second).Unix()
		case !k.Expires.IsZero():
			expires = k.Expires.Unix()
		}
		next := netscape.Set(c.jar, ".x.com", k.Name, k.Value, expires)
		if len(next) > netscape.MaxBytes {
			continue
		}
		c.jar = next
		c.cookies[k.Name] = k.Value
		if k.Name == "ct0" {
			c.csrf = k.Value
		}
		changed = true
	}
	jar := c.jar
	c.cookieMu.Unlock()
	if !changed {
		return
	}
	c.cfg.Log.Info("x_cookies_renewed", "provider", "x-web")
	if c.cfg.Save == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
		defer cancel()
		if err := c.cfg.Save(ctx, "cookies", string(jar)); err != nil {
			c.cfg.Log.Warn("x_cookies_save_failed", "provider", "x-web")
			return
		}
		c.cfg.Log.Info("x_cookies_saved", "provider", "x-web")
	}()
}
