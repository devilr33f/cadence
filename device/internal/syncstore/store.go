// Package syncstore keeps the last applied config of each capability across restarts.
package syncstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
)

type Store struct{ dir string }

// Entry is a capability's last applied config.
type Entry struct {
	Generation int64
	Config     json.RawMessage
	// Secrets came with the config: kept only here, in the store's mode-600 file
	Secrets map[string]string
}

type file struct {
	Generation int64             `json:"generation"`
	Config     json.RawMessage   `json:"config"`
	Secrets    map[string]string `json:"secrets,omitempty"`
}

var capabilityID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Dir is the store's directory in the daemon's data dir.
func Dir(dataDir string) string { return filepath.Join(dataDir, "config") }

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(capability string) (string, error) {
	if !capabilityID.MatchString(capability) {
		return "", errors.New("store: invalid capability id")
	}
	return filepath.Join(s.dir, capability+".json"), nil
}

// Load is LoadEntry without the secrets.
func (s *Store) Load(capability string) (gen int64, cfg json.RawMessage, ok bool, err error) {
	e, ok, err := s.LoadEntry(capability)
	return e.Generation, e.Config, ok, err
}

// Save is SaveEntry with no secrets.
func (s *Store) Save(capability string, gen int64, cfg json.RawMessage) error {
	return s.SaveEntry(capability, Entry{Generation: gen, Config: cfg})
}

func (s *Store) LoadEntry(capability string) (Entry, bool, error) {
	p, err := s.path(capability)
	if err != nil {
		return Entry{}, false, err
	}
	raw, err := os.ReadFile(p) //nolint:gosec // the id is validated and the dir is ours
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("load %s: %w", capability, err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return Entry{}, false, fmt.Errorf("load %s: corrupt file", capability)
	}
	return Entry(f), true, nil
}

// SaveEntry replaces the capability's file atomically (mode 600).
func (s *Store) SaveEntry(capability string, e Entry) error {
	p, err := s.path(capability)
	if err != nil {
		return err
	}
	b, err := json.Marshal(file(e))
	if err != nil {
		return fmt.Errorf("save %s: invalid config json", capability)
	}
	if err := atomicfile.Write(p, b); err != nil {
		return fmt.Errorf("save %s: %w", capability, err)
	}
	return nil
}

// deviceFile keeps the device's own info beside the configs: no capability id starts with an underscore
const deviceFile = "_device.json"

type device struct {
	Name string `json:"name"`
}

// LoadDeviceName is the device's name as SaveDeviceName kept it; ok is false while none is kept.
func (s *Store) LoadDeviceName() (name string, ok bool, err error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, deviceFile)) //nolint:gosec // a fixed name in our dir
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("load device: %w", err)
	}
	var d device
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", false, errors.New("load device: corrupt file")
	}
	return d.Name, true, nil
}

// SaveDeviceName keeps the device's name as the server last sent it, so the receivers advertise it from boot,
// before and without a link; it replaces the file atomically (mode 600) and goes with the configs at an enrollment.
func (s *Store) SaveDeviceName(name string) error {
	b, err := json.Marshal(device{Name: name})
	if err != nil {
		return errors.New("save device: invalid name")
	}
	if err := atomicfile.Write(filepath.Join(s.dir, deviceFile), b); err != nil {
		return fmt.Errorf("save device: %w", err)
	}
	return nil
}
