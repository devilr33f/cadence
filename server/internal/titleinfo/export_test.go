package titleinfo

// Unpaced drops the pace between requests, for tests.
func Unpaced(s *Service) { s.shiki.gap, s.jikan.gap = 0, 0 }
