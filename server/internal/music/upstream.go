package music

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Upstream resolves a song to its stream as the device's player would, for the server to fetch itself (an alarm's
// music, transcoded once when it is set): the song's YouTube Music video, then the player's signed url, which never
// leaves the server, reaches a log or is stored (D11).
func (s *Service) Upstream(ctx context.Context, deviceID string, t providers.Track) (string, error) {
	v, err := s.d.Settings(ctx, deviceID)
	if err != nil {
		return "", err
	}
	if v.MusicPlayer == "" {
		return "", ErrNotSetUp
	}
	p, err := s.d.Player(ctx, v.MusicPlayer)
	if err != nil {
		return "", ErrNotSetUp
	}
	if t, err = s.onYouTube(ctx, t); err != nil {
		return "", err
	}
	stream, err := p.Resolve(ctx, t.VideoID)
	if err != nil {
		return "", err
	}
	return stream.URL, nil
}
