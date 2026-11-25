package bluetooth

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// loadKnown reads the speakers that were active here, one short id a line, the most recently active last; a missing
// or unreadable file knows none, so a speaker is lowered once more rather than never (rule 7)
func loadKnown(path string) []string {
	var out []string
	if path == "" {
		return out
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the board's own file
	if err != nil {
		return out
	}
	for line := range strings.Lines(string(raw)) {
		if id := strings.TrimSpace(line); validID(id) {
			out = touchKnown(out, id)
		}
	}
	return out
}

// touchKnown moves id to the end, the most recently active
func touchKnown(known []string, id string) []string {
	return append(slices.DeleteFunc(known, func(k string) bool { return k == id }), id)
}

// saveKnown replaces the file whole: a temporary file renamed over it
func saveKnown(path string, ids []string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".known-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	var sb strings.Builder
	for _, id := range ids {
		sb.WriteString(id + "\n")
	}
	if _, err := tmp.WriteString(sb.String()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
