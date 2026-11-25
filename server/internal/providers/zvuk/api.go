package zvuk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	userAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	graphqlPath = "/api/v1/graphql"
	profilePath = "/api/tiny/profile"
)

type gqlError struct {
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

// graphql runs one operation and reads its data into v; errors that come
// with a 200 fail it, as a refusal when they say the token was refused
func (c *Client) graphql(ctx context.Context, op, query string, vars map[string]any, v any) error {
	body, err := json.Marshal(map[string]any{"operationName": op, "query": query, "variables": vars})
	if err != nil {
		return err
	}
	b, err := c.send(ctx, func(ctx context.Context, token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+graphqlPath, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Auth-Token", token)
		}
		return req, err
	}, func(b []byte) error {
		var a struct {
			Errors []gqlError `json:"errors"`
		}
		if json.Unmarshal(b, &a) != nil {
			return errBadAnswer
		}
		for _, e := range a.Errors {
			switch strings.ToUpper(e.Extensions.Code) {
			case "UNAUTHENTICATED", "UNAUTHORIZED", "FORBIDDEN":
				return statusError(http.StatusUnauthorized)
			}
		}
		if len(a.Errors) > 0 {
			return errGraphQL
		}
		return nil
	})
	if err != nil {
		return err
	}
	var a struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(b, &a) != nil || len(a.Data) == 0 || string(a.Data) == "null" || json.Unmarshal(a.Data, v) != nil {
		return errBadAnswer
	}
	return nil
}

// tiny reads one of the site's REST answers ({"result": ...}) into v
func (c *Client) tiny(ctx context.Context, path string, q url.Values, v any) error {
	b, err := c.send(ctx, func(ctx context.Context, token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+path+"?"+q.Encode(), nil)
		if err == nil {
			req.Header.Set("X-Auth-Token", token)
		}
		return req, err
	}, nil)
	if err != nil {
		return err
	}
	var a struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(b, &a) != nil || len(a.Result) == 0 || json.Unmarshal(a.Result, v) != nil {
		return errBadAnswer
	}
	return nil
}

// send makes the request with the token and reads its answer; a guest
// token the site refuses (401 or 403, check's too) is fetched again once
func (c *Client) send(ctx context.Context, build func(context.Context, string) (*http.Request, error), check func([]byte) error) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		token, err := c.token(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		req, err := build(ctx, token)
		if err != nil {
			return nil, err
		}
		b, err := c.do(req)
		if err == nil && check != nil {
			err = check(b)
		}
		if refused(err) && attempt == 0 && c.cfg.Token == "" {
			continue
		}
		return b, err
	}
}

func refused(err error) bool {
	return errors.Is(err, statusError(http.StatusUnauthorized)) || errors.Is(err, statusError(http.StatusForbidden))
}

// token is the signed-in one when set, else a guest's, fetched when missing
// or refused
func (c *Client) token(ctx context.Context, refresh bool) (string, error) {
	if c.cfg.Token != "" {
		return c.cfg.Token, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !refresh && c.guest != "" {
		return c.guest, nil
	}
	c.guest = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+profilePath, nil)
	if err != nil {
		return "", err
	}
	b, err := c.do(req)
	if err != nil {
		return "", err
	}
	var a struct {
		Result struct {
			Token string `json:"token"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &a) != nil {
		return "", errBadAnswer
	}
	if !tokenRe.MatchString(a.Result.Token) {
		return "", errNoToken
	}
	c.guest = a.Result.Token
	return c.guest, nil
}

// do sends req with the site's browser headers; its anti-bot answers (418,
// or an html page in place of json) fail it
func (c *Client) do(req *http.Request) ([]byte, error) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	req.Header.Set("Referer", "https://zvuk.com/")
	req.Header.Set("Origin", "https://zvuk.com")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	if t := bytes.TrimSpace(b); len(t) == 0 || t[0] == '<' {
		return nil, errBlocked
	}
	return b, nil
}
