package sim

import (
	"path/filepath"

	"git.shino.boo/devilreef/cadence/device/internal/airplay"
)

// airplayOptions keeps the receiver's runtime under the data dir and its
// scratch beside audiod's socket; the sim has no real receiver to run
func (s *sim) airplayOptions() airplay.Options {
	return airplay.Options{
		Dir:        filepath.Join(s.dataDir, "airplay"),
		ScratchDir: filepath.Join(s.dataDir, "run", "airplay"),
	}
}
