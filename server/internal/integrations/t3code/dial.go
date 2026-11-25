package t3code

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/coder/websocket"
)

const maxBaseURL = 2048

// endpoints are the base URL's HTTP origin and path prefix (no trailing
// slash) and the WebSocket URL of /ws with the protocol query.
type endpoints struct {
	http string
	ws   string
}

func parseBase(raw string) (endpoints, error) {
	if len(raw) > maxBaseURL || strings.ContainsAny(raw, "?#") || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return endpoints{}, ErrBadURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return endpoints{}, ErrBadURL
	}
	var scheme string
	switch u.Scheme {
	case "http":
		scheme = "ws"
	case "https":
		scheme = "wss"
	default:
		return endpoints{}, ErrBadURL
	}
	prefix := strings.TrimRight(u.EscapedPath(), "/")
	return endpoints{
		http: u.Scheme + "://" + u.Host + prefix,
		ws:   scheme + "://" + u.Host + prefix + "/ws?orchestrationProtocol=" + ProtocolVersion,
	}, nil
}

// dial opens /ws with the bearer token on the upgrade; when the upgrade is
// refused 401 (a proxy that drops the header, a server that wants a ticket),
// it buys a websocket ticket with the token and dials /ws?wsTicket=. It
// returns a code on failure, never the URL or the token.
func dial(ctx context.Context, ep endpoints, client *http.Client, token string, t timings) (*websocket.Conn, string) {
	hctx, cancel := context.WithTimeout(ctx, t.handshake)
	defer cancel()
	ws, status := upgrade(hctx, ep.ws, client, http.Header{"Authorization": {"Bearer " + token}})
	if ws != nil {
		ws.SetReadLimit(t.readLimit)
		return ws, ""
	}
	if status != http.StatusUnauthorized {
		return nil, dialCode(hctx, status)
	}
	ticket, code := websocketTicket(hctx, ep.http, client, token)
	if code != "" {
		return nil, code
	}
	ws, status = upgrade(hctx, ep.ws+"&wsTicket="+url.QueryEscape(ticket), client, nil)
	if ws == nil {
		return nil, dialCode(hctx, status)
	}
	ws.SetReadLimit(t.readLimit)
	return ws, ""
}

func upgrade(ctx context.Context, u string, client *http.Client, h http.Header) (*websocket.Conn, int) {
	ws, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: client, HTTPHeader: h})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		return ws, 0
	}
	if resp != nil {
		return nil, resp.StatusCode
	}
	return nil, 0
}

func dialCode(ctx context.Context, status int) string {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return CodeAuthFailed
	case status == http.StatusUpgradeRequired:
		return CodeProtocol
	case status != 0:
		return CodeBadResponse
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return CodeTimeout
	}
	return CodeUnavailable
}

// websocketTicket is POST /api/auth/websocket-ticket's {"ticket"}.
func websocketTicket(ctx context.Context, base string, client *http.Client, token string) (string, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/auth/websocket-ticket", nil)
	if err != nil {
		return "", CodeBadResponse
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", dialCode(ctx, 0)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", dialCode(ctx, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", dialCode(ctx, 0)
	}
	var r struct {
		Ticket string `json:"ticket"`
	}
	if json.Unmarshal(bytes.TrimSpace(b), &r) != nil || strings.TrimSpace(r.Ticket) == "" {
		return "", CodeBadResponse
	}
	return r.Ticket, ""
}
