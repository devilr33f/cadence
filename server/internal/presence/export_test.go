package presence

import "context"

// Expire runs the expiry pass now, as the loop does at a signal's time.
func (s *Service) Expire(ctx context.Context) { s.expire(ctx) }
