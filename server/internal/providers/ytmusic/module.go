package ytmusic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Module needs no key: the search is the music site's own. The host is fixed.
var Module = providers.Module{
	ID:           "youtube-music",
	Kind:         providers.KindMusic,
	ConfigSchema: `{"type":"object","additionalProperties":false}`,
	Probe:        probe,
	New: func(json.RawMessage, map[string]string) (any, error) {
		return New(Config{}, httpx.Client(nil)), nil
	},
}

// probe runs one search, which costs nothing, and wants an answer it reads
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	resp, err := c.post(ctx, "test")
	if err != nil {
		switch {
		case errors.Is(err, httpx.ErrRedirect):
			return providers.ProbeBadResponse
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return providers.ProbeTimeout
		}
		return providers.ProbeUnreachable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return providers.ProbeStatus(resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return providers.ProbeBadResponse
	}
	if _, ok := parseSongs(b); !ok {
		return providers.ProbeBadResponse
	}
	return nil
}
