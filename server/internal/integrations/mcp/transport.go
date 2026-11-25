package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// maxBody caps every response body: the SDK reads a JSON body whole, uncapped.
const maxBody = 1 << 20

var (
	errEndpoint     = errors.New("mcp: request outside the endpoint refused")
	errUnauthorized = errors.New("mcp: credential rejected")
	errForbidden    = errors.New("mcp: request forbidden")
	// the statuses the SDK treats as transient, keeping the connection
	errTransient = errors.New("mcp: server busy or failing")
	// a 404 to a request in a session: the server no longer knows it, whatever
	// the body says; the SDK reads a JSON-RPC error body as the answer instead
	errSessionGone = errors.New("mcp: session gone")
	errTooLarge    = errors.New("mcp: response body over the cap")
)

// sessionHeader is the legacy era's session id header.
const sessionHeader = "Mcp-Session-Id"

// overKey carries a request's own body-cap flag in its context: the SDK
// reports a failed body read only as text, after the request has returned.
type overKey struct{}

// withOverflow gives the requests made under ctx a flag that is set when one
// of their bodies goes over the cap.
func withOverflow(ctx context.Context) (context.Context, *atomic.Bool) {
	over := new(atomic.Bool)
	return context.WithValue(ctx, overKey{}, over), over
}

// pinned carries the SDK's requests: only to the configured endpoint, with
// the Authorization header when one is configured, every response body
// capped. One serves one session.
type pinned struct {
	base     http.RoundTripper
	endpoint *url.URL
	auth     string
	// life ends every request still running and fails the ones that follow,
	// such as the SDK's cancel notices, which closing otherwise waits 5 s for
	life context.Context
	kill context.CancelFunc
}

func newPinned(base http.RoundTripper, endpoint *url.URL, auth string) *pinned {
	life, kill := context.WithCancel(context.Background())
	return &pinned{base: base, endpoint: endpoint, auth: auth, life: life, kill: kill}
}

// httpClient refuses every redirect, whatever base's own client does.
func (p *pinned) httpClient() *http.Client {
	return &http.Client{
		Transport:     p,
		CheckRedirect: func(*http.Request, []*http.Request) error { return httpx.ErrRedirect },
	}
}

func (p *pinned) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL
	if u.Scheme != p.endpoint.Scheme || u.Host != p.endpoint.Host || u.EscapedPath() != p.endpoint.EscapedPath() ||
		u.RawQuery != "" || u.Fragment != "" || u.User != nil || (req.Host != "" && req.Host != u.Host) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errEndpoint
	}
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(p.life, cancel)
	done := func() {
		stop()
		cancel()
	}
	r := req.Clone(ctx)
	r.Header.Del("Authorization")
	if p.auth != "" {
		r.Header.Set("Authorization", p.auth)
	}
	resp, err := p.base.RoundTrip(r)
	if err != nil {
		done()
		return nil, err
	}
	var refused error
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		refused = errUnauthorized
	case http.StatusForbidden:
		refused = errForbidden
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		refused = errTransient
	case http.StatusNotFound:
		if req.Header.Get(sessionHeader) != "" {
			refused = errSessionGone
		}
	}
	if refused != nil {
		_ = resp.Body.Close()
		done()
		return nil, refused
	}
	over, _ := req.Context().Value(overKey{}).(*atomic.Bool)
	resp.Body = &capped{rc: resp.Body, over: over, done: done}
	return resp, nil
}

// capped fails a read past maxBody with errTooLarge and sets over.
type capped struct {
	rc   io.ReadCloser
	read int64
	over *atomic.Bool
	done func()
}

func (b *capped) Read(p []byte) (int, error) {
	if b.read > maxBody {
		return 0, errTooLarge
	}
	if room := maxBody + 1 - b.read; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := b.rc.Read(p)
	b.read += int64(n)
	if b.read > maxBody {
		if b.over != nil {
			b.over.Store(true)
		}
		return n - int(b.read-maxBody), errTooLarge
	}
	return n, err
}

func (b *capped) Close() error {
	err := b.rc.Close()
	b.done()
	return err
}
