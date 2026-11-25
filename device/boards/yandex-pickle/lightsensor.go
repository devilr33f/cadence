package pickle

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const iioDevices = "/sys/bus/iio/devices"

// findIIO is the iio device whose name is name, whatever index the kernel gave it
func findIIO(root, name string) (string, bool) {
	dir := under(root, iioDevices)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name(), "name")) //nolint:gosec // sysfs, under the board's root
		if err == nil && strings.TrimSpace(string(raw)) == name {
			return filepath.Join(dir, e.Name()), true
		}
	}
	return "", false
}

// Light is the sensor's raw reading: 0 in the dark, 4 under evening room lights. The sensor is looked up by name at
// the first reading and again after one fails, as a rebound driver may come back under another index.
func (s *lightSink) Light() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sensor == "" {
		dir, ok := findIIO(s.root, lightSensor)
		if !ok {
			return 0, false
		}
		s.sensor = filepath.Join(dir, "in_intensity_raw")
	}
	raw, err := os.ReadFile(s.sensor)
	if err != nil {
		s.sensor = ""
		return 0, false
	}
	// one line, the number alone
	v, err := strconv.Atoi(strings.TrimSuffix(string(raw), "\n"))
	if err != nil {
		return 0, false
	}
	return v, true
}
