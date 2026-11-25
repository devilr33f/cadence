package vkmusic

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// StreamTrack answers a stream of a song this provider found. Its url is read
// again right before play (vk's urls live a short while): an HLS playlist of
// AES-128 segments, or an older plain mp3. A song vk blocks is ErrUnplayable;
// a token vk refuses, or one whose client lost audio access, is
// ErrSignInRequired.
func (c *Client) StreamTrack(ctx context.Context, t providers.Track) (providers.Stream, error) {
	if t.Source != providers.SourceVK || !idRe.MatchString(t.ID) {
		return providers.Stream{}, providers.ErrUnplayable
	}
	var out providers.Stream
	err := c.guard(func() error {
		var err error
		out, err = c.stream(ctx, t.ID)
		return err
	})
	var api apiError
	switch {
	case err == nil:
		return out, nil
	case errors.Is(err, providers.ErrUnplayable), errors.Is(err, providers.ErrSignInRequired):
		return providers.Stream{}, err
	case errors.As(err, &api) && api.signIn():
		return providers.Stream{}, providers.ErrSignInRequired
	}
	return providers.Stream{}, errors.New(providers.CodePlayerFailed)
}

func (c *Client) stream(ctx context.Context, id string) (providers.Stream, error) {
	var items []audioItem
	if err := c.call(ctx, "audio.getById", url.Values{"audios": {id}}, &items); err != nil {
		return providers.Stream{}, err
	}
	if len(items) == 0 || items[0].URL == "" || items[0].ContentRestricted != 0 {
		return providers.Stream{}, providers.ErrUnplayable
	}
	it := items[0]
	// vk hands a client without audio access a stub that says so
	if strings.Contains(it.URL, "audio_api_unavailable") {
		return providers.Stream{}, providers.ErrSignInRequired
	}
	u, err := url.Parse(it.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return providers.Stream{}, errBadAnswer
	}
	return providers.Stream{URL: it.URL, HLS: strings.HasSuffix(u.Path, ".m3u8"), DurationS: max(it.Duration, 0)}, nil
}

// TrackRadio answers up to limit songs vk recommends after the seed.
func (c *Client) TrackRadio(ctx context.Context, seed providers.Track, limit int) ([]providers.Track, error) {
	if seed.Source != providers.SourceVK || !idRe.MatchString(seed.ID) || limit < 1 {
		return nil, errFailed
	}
	base := baseID(seed.ID)
	var out []providers.Track
	if c.guard(func() error {
		var a itemsAnswer
		q := url.Values{"target_audio": {base}, "count": {strconv.Itoa(min(limit+1, 100))}}
		if err := c.call(ctx, "audio.getRecommendations", q, &a); err != nil {
			return err
		}
		out = tracks(a.Items, limit, base)
		return nil
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}
