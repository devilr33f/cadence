package zvuk

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const streamPath = "/api/tiny/track/stream"

// qualities is the order streams are tried in for the configured quality:
// never flacdrm, which the device cannot decrypt
func (c *Client) qualities() []string {
	switch c.cfg.Quality {
	case QualityFLAC:
		return []string{QualityFLAC, QualityHigh, QualityMid}
	case QualityMid:
		return []string{QualityMid}
	}
	return []string{QualityHigh, QualityMid}
}

// StreamTrack answers a progressive stream of a song this provider found, in
// the configured quality or the best below it Zvuk gives this token. A
// refused token is ErrSignInRequired, a song with no stream ErrUnplayable.
func (c *Client) StreamTrack(ctx context.Context, t providers.Track) (providers.Stream, error) {
	if t.Source != providers.SourceZvuk || !idRe.MatchString(t.ID) {
		return providers.Stream{}, providers.ErrUnplayable
	}
	var u string
	err := c.guard(func() error {
		var err error
		u, err = c.stream(ctx, t.ID)
		return err
	})
	switch {
	case err == nil:
		return providers.Stream{URL: u, DurationS: max(t.DurationS, 0)}, nil
	case errors.Is(err, providers.ErrUnplayable):
		return providers.Stream{}, err
	case refused(err):
		return providers.Stream{}, providers.ErrSignInRequired
	case errors.Is(err, statusError(http.StatusNotFound)):
		return providers.Stream{}, providers.ErrUnplayable
	}
	return providers.Stream{}, errors.New(providers.CodePlayerFailed)
}

// stream asks GraphQL for every quality's url at once; the site's REST
// route, which its anti-bot often refuses, is asked only when GraphQL gives
// none
func (c *Client) stream(ctx context.Context, id string) (string, error) {
	var a struct {
		MediaContents []struct {
			Typename string `json:"__typename"`
			Stream   *struct {
				FLAC string `json:"flac"`
				High string `json:"high"`
				Mid  string `json:"mid"`
			} `json:"stream"`
		} `json:"mediaContents"`
	}
	gqlErr := c.graphql(ctx, "getStream", streamQuery, map[string]any{"ids": []string{id}}, &a)
	if refused(gqlErr) || ctx.Err() != nil {
		return "", gqlErr
	}
	if gqlErr == nil {
		for _, m := range a.MediaContents {
			if s := m.Stream; s != nil && (m.Typename == "" || m.Typename == "Track") {
				byQuality := map[string]string{QualityFLAC: s.FLAC, QualityHigh: s.High, QualityMid: s.Mid}
				for _, q := range c.qualities() {
					if playable(byQuality[q]) {
						return byQuality[q], nil
					}
				}
			}
		}
	}
	for _, q := range c.qualities() {
		var r struct {
			Stream string `json:"stream"`
		}
		err := c.tiny(ctx, streamPath, url.Values{"id": {id}, "quality": {q}}, &r)
		if err == nil && playable(r.Stream) {
			return r.Stream, nil
		}
		if refused(err) {
			return "", err
		}
	}
	if gqlErr != nil {
		return "", gqlErr
	}
	return "", providers.ErrUnplayable
}

func playable(u string) bool {
	return strings.HasPrefix(u, "https://") && len(u) <= 4096
}
