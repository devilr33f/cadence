package music

import (
	"context"
	"errors"
	"math/rand/v2"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// StartRadio plays a new queue of the seed and its YouTube Music radio, which
// tops itself up at its end; a song another catalog found is matched on
// YouTube Music first. It answers how many songs the queue holds. Without a
// radio (its fetch failed) the seed plays alone and the radio is fetched
// again at its end.
func (s *Service) StartRadio(ctx context.Context, deviceID string, seed providers.Track) (int, error) {
	defer s.playLock(deviceID)()
	more := s.nativeRadio(ctx, seed)
	if more == nil {
		var err error
		if seed, err = s.onYouTube(ctx, seed); err != nil {
			return 0, err
		}
		more = s.radioOf(ctx, seed)
	}
	tracks := append([]providers.Track{seed}, unseen([]providers.Track{seed}, more)...)
	if err := s.playNew(ctx, deviceID, tracks, 0, Options{Radio: true}); err != nil {
		return 0, err
	}
	return min(len(tracks), MaxQueue), nil
}

// RadioFromCurrent keeps the queue's song and what played before it, and
// puts the song's radio after it in place of the rest: the song plays on,
// or plays again when it no longer does. It answers how many songs the
// radio added.
func (s *Service) RadioFromCurrent(ctx context.Context, deviceID string) (int, error) {
	defer s.playLock(deviceID)()
	s.mu.Lock()
	q := s.queues[deviceID]
	cur, ok := q.current()
	s.mu.Unlock()
	if !ok {
		return 0, ErrNoQueue
	}
	more := s.nativeRadio(ctx, cur)
	if more == nil {
		var err error
		if cur, err = s.onYouTube(ctx, cur); err != nil {
			return 0, err
		}
		more = s.radioOf(ctx, cur)
	}
	if len(more) == 0 {
		return 0, errors.New(providers.CodeMusicFailed)
	}
	s.mu.Lock()
	q.setCurrent(cur)
	n := q.replaceUpcoming(more)
	q.radio = true
	s.mu.Unlock()
	if !s.Owns(deviceID) {
		if err := s.playSkipping(ctx, deviceID, q, 1); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// radioOf is the song's radio: its own provider's, else its YouTube video's;
// nil when it could not be read. A station never falls back to YouTube radio.
func (s *Service) radioOf(ctx context.Context, seed providers.Track) []providers.Track {
	if more := s.nativeRadio(ctx, seed); more != nil {
		return more
	}
	if seed.Station != "" {
		return nil
	}
	if s.d.YouTube == nil || seed.VideoID == "" {
		return nil
	}
	more, err := s.d.YouTube.Radio(ctx, seed.VideoID, radioBatch)
	if err != nil {
		s.d.Log.Warn("music_radio_failed", "code", providers.CodeMusicFailed)
		return nil
	}
	return more
}

// nativeRadio is the radio of a song its own provider found, when that
// provider has one (a TrackRadio); its songs play from the same instance.
// A station survives its song's YouTube match: the next batch is still that
// station's. nil when there is none or it could not be read.
func (s *Service) nativeRadio(ctx context.Context, seed providers.Track) []providers.Track {
	if seed.Instance == "" || (seed.VideoID != "" && seed.Station == "") || s.d.Music == nil {
		return nil
	}
	m, err := s.d.Music(ctx, seed.Instance)
	if err != nil {
		return nil
	}
	r, ok := m.(providers.TrackRadio)
	if !ok {
		return nil
	}
	more, err := r.TrackRadio(ctx, seed, radioBatch)
	if err != nil || len(more) == 0 {
		s.d.Log.Warn("music_radio_failed", "source", seed.Source, "code", providers.CodeMusicFailed)
		return nil
	}
	for i := range more {
		more[i].Instance = seed.Instance
	}
	return more
}

// replaceUpcoming keeps the songs up to the playing one, in play order, and
// puts more after them; the oldest go first when they would not fit. It
// answers how many of more it kept.
func (q *queue) replaceUpcoming(more []providers.Track) int {
	kept := make([]providers.Track, 0, q.pos+1)
	for i := 0; i <= q.pos && i < len(q.order); i++ {
		kept = append(kept, q.items[q.order[i]])
	}
	more = unseen(kept, more)
	if over := len(kept) + len(more) - MaxQueue; over > 0 {
		kept = kept[min(over, len(kept)-1):]
		more = more[:min(len(more), MaxQueue-len(kept))]
	}
	q.items = append(kept, more...)
	q.order = make([]int, len(q.items))
	for i := range q.order {
		q.order[i] = i
	}
	q.pos = len(kept) - 1
	if q.shuffle {
		rest := q.order[q.pos+1:]
		rand.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] }) //nolint:gosec // a play order is no secret
	}
	return len(more)
}
