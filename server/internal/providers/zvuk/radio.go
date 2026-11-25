package zvuk

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// the most related releases read for one radio
const maxRelated = 25

// TrackRadio answers up to limit songs like the seed: Zvuk has no song
// radio for an app, so, as Music Assistant's provider does, the releases
// Zvuk relates to the seed's release are read and their songs taken in turn,
// first songs first.
func (c *Client) TrackRadio(ctx context.Context, seed providers.Track, limit int) ([]providers.Track, error) {
	if seed.Source != providers.SourceZvuk || !idRe.MatchString(seed.ID) || limit < 1 {
		return nil, errFailed
	}
	var out []providers.Track
	if c.guard(func() error {
		var err error
		out, err = c.radio(ctx, seed.ID, limit)
		return err
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

func (c *Client) radio(ctx context.Context, seedID string, limit int) ([]providers.Track, error) {
	var a struct {
		GetTracks []struct {
			ID      zid `json:"id"`
			Release *struct {
				ID zid `json:"id"`
			} `json:"release"`
		} `json:"getTracks"`
	}
	if err := c.graphql(ctx, "getTracks", tracksQuery, map[string]any{"ids": []string{seedID}}, &a); err != nil {
		return nil, err
	}
	if len(a.GetTracks) == 0 || a.GetTracks[0].Release == nil || !idRe.MatchString(string(a.GetTracks[0].Release.ID)) {
		return nil, nil
	}
	own, err := c.releases(ctx, []string{string(a.GetTracks[0].Release.ID)}, min(max(limit, 5), maxRelated))
	if err != nil || len(own) == 0 {
		return nil, err
	}
	var ids []string
	for _, r := range own[0].Related {
		if idRe.MatchString(string(r.ID)) && len(ids) < maxRelated {
			ids = append(ids, string(r.ID))
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	related, err := c.releases(ctx, ids, 1)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{seedID: true}
	var out []providers.Track
	for round, more := 0, true; more && len(out) < limit; round++ {
		more = false
		for _, r := range related {
			songs := r.songs()
			if round >= len(songs) {
				continue
			}
			more = true
			if t, ok := track(songs[round]); ok && !seen[t.ID] && len(out) < limit {
				seen[t.ID] = true
				out = append(out, t)
			}
		}
	}
	return out, nil
}
