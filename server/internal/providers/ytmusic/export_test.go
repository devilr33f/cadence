package ytmusic

import "git.shino.boo/devilreef/cadence/server/internal/providers"

// ParseSongs exposes the parser for its own tests.
func ParseSongs(b []byte) ([]providers.Track, bool) { return parseSongs(b) }
