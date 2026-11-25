package commands

import "time"

func SetTimeout(s *Service, d time.Duration) { s.timeout = d }

// SetClock is the clock session grants expire by.
func SetClock(s *Service, now func() time.Time) { s.now = now }
