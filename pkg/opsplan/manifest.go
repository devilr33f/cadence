package opsplan

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// FileEntry is one line of a device tarball's MANIFEST: pack-device.sh writes plain sha256sum
// output, relative to the tree's root.
type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

// Manifest is a device tarball's MANIFEST: its headers (the first is "# cadence <version>") and
// its per-file sha256 lines. Mode and Size do not travel in the MANIFEST: the artifact store
// fills them from the tar entries as it parses.
type Manifest struct {
	Headers map[string]string
	Files   []FileEntry
}

// Version is the cadence header: the build the tarball carries.
func (m *Manifest) Version() string { return m.Headers["cadence"] }

var manifestLine = regexp.MustCompile(`^([0-9a-f]{64})  (.+)$`)

// ParseManifest reads a MANIFEST's bytes; the header names it for errors.
func ParseManifest(data []byte) (Manifest, error) {
	m := Manifest{Headers: map[string]string{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if h, ok := strings.CutPrefix(line, "# "); ok {
			key, val, found := strings.Cut(h, " ")
			if !found {
				return m, fmt.Errorf("manifest: bad header %q", line)
			}
			m.Headers[key] = val
			continue
		}
		f := manifestLine.FindStringSubmatch(line)
		if f == nil {
			return m, fmt.Errorf("manifest: bad file line %q", line)
		}
		m.Files = append(m.Files, FileEntry{Path: f[2], SHA256: f[1]})
	}
	if err := sc.Err(); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	if m.Version() == "" {
		return m, fmt.Errorf("manifest: no cadence header")
	}
	if len(m.Files) == 0 {
		return m, fmt.Errorf("manifest: no files")
	}
	return m, nil
}

// CleanTail is an exec output tail as it is kept: carriage returns gone, cut to at most limit bytes
// on a line boundary, its ends trimmed.
func CleanTail(b []byte, limit int) string {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	if len(b) > limit {
		cut := b[len(b)-limit:]
		if i := bytes.IndexByte(cut, '\n'); i >= 0 {
			cut = cut[i+1:]
		}
		b = cut
	}
	return strings.TrimSpace(string(b))
}
