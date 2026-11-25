// Package volume owns the device's output level: it keeps it across restarts,
// sets it for commands and local senders, and applies rule 7 to raises.
package volume

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
)

const (
	minLevel = 0
	maxLevel = 100
)

// Store keeps the level in <dataDir>/state/volume. A nil Store keeps nothing.
type Store struct{ path string }

func NewStore(dataDir string) *Store {
	return &Store{path: filepath.Join(dataDir, "state", "volume")}
}

type file struct {
	Level *int `json:"level"`
}

// Load returns the stored level and whether there is one: a missing,
// unreadable or corrupt file leaves the level unknown.
func (s *Store) Load() (int, bool) {
	if s == nil {
		return 0, false
	}
	raw, err := os.ReadFile(s.path) //nolint:gosec // a fixed name in the daemon's data dir
	if err != nil {
		return 0, false
	}
	return ParseLevel(raw)
}

// ParseLevel reads a volume file's content as Load does: the installer
// converts levels with the same rule.
func ParseLevel(raw []byte) (int, bool) {
	var f file
	if json.Unmarshal(raw, &f) != nil || f.Level == nil || *f.Level < minLevel || *f.Level > maxLevel {
		return 0, false
	}
	return *f.Level, true
}

// EncodeLevel is the file's content for level, as Save writes it.
func EncodeLevel(level int) []byte { return fmt.Appendf(nil, `{"level":%d}`, level) }

// Save replaces the file atomically (mode 600).
func (s *Store) Save(level int) error {
	if s == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(s.path, EncodeLevel(level))
}
