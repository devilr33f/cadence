package vkcapsula

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// findIIO is the iio device whose name is name, whatever index the kernel gave it
func findIIO(root, name string) (string, bool) {
	dir := under(root, "/sys/bus/iio/devices")
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

// Light is the bh1730's lux, looked up by name at the first reading and again after one fails
func (s *lightSink) Light() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sensor == "" {
		dir, ok := findIIO(s.root, "bh1730")
		if !ok {
			return 0, false
		}
		s.sensor = filepath.Join(dir, "in_illuminance_both_input")
	}
	raw, err := os.ReadFile(s.sensor) //nolint:gosec // sysfs, under the board's root
	if err != nil {
		s.sensor = ""
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return int(v), true
}
