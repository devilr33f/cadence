package ha

import (
	"context"
	"errors"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// probe opens a connection of its own, authenticates and reads the core
// config (get_config, read only), then closes it: no service is called, and
// the client's own connection, if it runs one, is left alone. A wrong token
// is one failed login; the test's own limit keeps Home Assistant from
// banning the server for it.
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	hctx, cancel := context.WithTimeout(ctx, c.t.handshake)
	defer cancel()
	ws, resp, err := websocket.Dial(hctx, c.url, &websocket.DialOptions{HTTPClient: c.http})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	switch {
	case err != nil && resp != nil:
		return providers.ProbeStatus(resp.StatusCode)
	case errors.Is(err, httpx.ErrRedirect):
		return providers.ProbeBadResponse
	case err != nil:
		return probeCode(transportCode(hctx))
	}
	ws.SetReadLimit(c.t.readLimit)
	if _, code := authenticate(hctx, ws, c.token); code != "" {
		_ = ws.CloseNow()
		return probeCode(code)
	}
	life, end := context.WithCancel(ctx)
	defer end()
	cn := newConn(life, ws, c.log, c.t)
	cn.start()
	defer func() {
		cn.close(CodeUnavailable)
		cn.wg.Wait()
	}()
	cctx, cancelCmd := context.WithTimeout(ctx, c.t.command)
	defer cancelCmd()
	m, err := cn.exchange(cctx, "get_config", nil, nil)
	if err == nil {
		_, err = outcome(m)
	}
	switch code := cn.closedCode(); {
	case err == nil:
		return nil
	case code != "":
		// the connection failed under the command: its own reason
		return probeCode(code)
	}
	return probeCode(Code(err))
}

func probeCode(code string) error {
	switch code {
	case CodeAuthFailed:
		return providers.ProbeAuth
	case CodeTimeout:
		return providers.ProbeTimeout
	case CodeUnavailable:
		return providers.ProbeUnreachable
	}
	return providers.ProbeBadResponse
}
