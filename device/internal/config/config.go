package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
)

type Config struct {
	Board       string `json:"board"`
	ServerURL   string `json:"serverUrl,omitempty"`
	DeviceID    string `json:"deviceId,omitempty"`
	DeviceToken string `json:"deviceToken,omitempty"`
	DataDir     string `json:"dataDir"`
	CAFile      string `json:"caFile,omitempty"`
	// Audiod runs the sim board's audio through audiod.
	Audiod       bool   `json:"audiod,omitempty"`
	AudiodBinary string `json:"audiodBinary,omitempty"`
}

const defaultAudiodBinary = "/usr/lib/cadence/cadence-audiod"

var (
	boardID = regexp.MustCompile(`^[a-z0-9-]+$`)
	// lowercase: encoding/json matches field names case-insensitively
	knownKeys = map[string]bool{
		"board": true, "serverurl": true, "deviceid": true, "devicetoken": true, "datadir": true, "cafile": true,
		"audiod": true, "audiodbinary": true,
	}
)

// AudiodExecutable is the audiod binary, the default one when none is set.
func (c Config) AudiodExecutable() string {
	if c.AudiodBinary == "" {
		return defaultAudiodBinary
	}
	return c.AudiodBinary
}

// Load returns the config and the number of unknown top-level keys, which a newer config may carry and are ignored.
// errors name fields only: a config holds the device token
func Load(path string) (Config, int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the operator's own --config flag
	if err != nil {
		return Config{}, 0, fmt.Errorf("read config: %w", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return Config{}, 0, errors.New("parse config: not a json object")
	}
	unknown := 0
	for k := range keys {
		if !knownKeys[strings.ToLower(k)] {
			unknown++
		}
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return Config{}, 0, fmt.Errorf("config: %s has the wrong type", te.Field)
		}
		return Config{}, 0, errors.New("parse config: invalid json")
	}
	if err := Validate(cfg); err != nil {
		return Config{}, 0, err
	}
	return cfg, unknown, nil
}

func Validate(c Config) error {
	if !boardID.MatchString(c.Board) {
		return errors.New("config: board must match ^[a-z0-9-]+$")
	}
	if c.DataDir == "" {
		return errors.New("config: dataDir is required")
	}
	// the device is linux: a posix path, whatever the host the config is checked on
	if c.AudiodBinary != "" && !path.IsAbs(c.AudiodBinary) {
		return errors.New("config: audiodBinary must be an absolute path")
	}
	if err := ValidateServerURL(c.Board, c.ServerURL); err != nil {
		return err
	}
	if c.ServerURL != "" {
		if c.DeviceID == "" {
			return errors.New("config: deviceId is required with serverUrl")
		}
		if c.DeviceToken == "" {
			return errors.New("config: deviceToken is required with serverUrl")
		}
	}
	return nil
}

// ValidateServerURL accepts https, and http only on the simulators (sim, sim-tv); empty means offline.
func ValidateServerURL(board, u string) error {
	switch {
	case u == "", strings.HasPrefix(u, "https://") && len(u) > len("https://"):
		return nil
	case strings.HasPrefix(u, "http://") && len(u) > len("http://") && (board == "sim" || board == "sim-tv"):
		return nil
	}
	return errors.New("config: serverUrl must be https:// (http:// only on the sim boards)")
}

func Save(path string, c Config) error {
	commit, _, err := Stage(path, c)
	if err != nil {
		return err
	}
	if err := commit(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Stage writes the config to a temp file; commit makes it the config, abort drops it.
func Stage(path string, c Config) (commit func() error, abort func(), err error) {
	if err := Validate(c); err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, nil, errors.New("encode config failed")
	}
	commit, abort, err = atomicfile.Stage(path, b, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("write config: %w", err)
	}
	return commit, abort, nil
}
