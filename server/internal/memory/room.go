package memory

import (
	"context"
	"errors"
)

// MakeRoom makes space for one more active memory in a scope (deviceID empty
// for global) by evicting the learned ones that score lowest, never pinned,
// manual or listed in keep. It returns how many went and whether one more
// now fits.
func (s *Store) MakeRoom(ctx context.Context, scope, deviceID string, keep []string) (evicted int, fits bool, err error) {
	n, err := s.CountByScope(ctx, scope, deviceID)
	if err != nil {
		return 0, false, err
	}
	if n < activeCap {
		return 0, true, nil
	}
	cands, err := s.EvictionCandidates(ctx, scope, deviceID, n-activeCap+1, keep)
	if err != nil {
		return 0, false, err
	}
	for _, m := range cands {
		switch err := s.Delete(ctx, m.ID); {
		case err == nil:
			evicted++
			n--
		case errors.Is(err, ErrNotFound):
			n--
		}
	}
	return evicted, n < activeCap, nil
}
