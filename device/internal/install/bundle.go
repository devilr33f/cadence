package install

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Bundle is a device tarball (make device-build) checked on the host before anything reaches the device: MANIFEST
// first, naming the board and arch, then only directories and the regular files it lists, each with its sha256.
type Bundle struct {
	Path    string
	Size    int64
	Sum     string
	Version string
	// Wrapper is share/cadence-run and Hook share/board-hook, the board's part it sources from beside itself: the
	// installer runs them to blank the lights before the tree is unpacked
	Wrapper, Hook []byte
	// CA is share/ca-certificates.crt, the bundle an owner's extra CA is added to
	CA []byte
	// Top are the tarball's top-level entries, MANIFEST included
	Top []string
}

// the top-level entries a tarball may hold, and the one directory each of soloist/ and airplay/ may
var (
	bundleTop      = map[string]bool{"MANIFEST": true, "bin": true, "share": true, "licenses": true, "soloist": true, "airplay": true}
	bundleRuntimes = map[string]bool{"soloist": true, "airplay": true}
	manifestLine   = regexp.MustCompile(`^([0-9a-f]{64})  (.+)$`)
)

// required are what the board and the installer run from the tree
var required = []string{
	"bin/cadenced", "bin/cadence-audiod", "share/ca-certificates.crt", "share/cadence-run", "share/board-hook",
}

// OpenBundle checks the tarball at path for board and arch and returns what the installer needs of it.
func OpenBundle(file, board, arch string) (*Bundle, error) {
	f, err := os.Open(file) //nolint:gosec // the owner's own tarball
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	whole := sha256.New()
	counted := &countingReader{r: io.TeeReader(f, whole)}
	b, err := readBundle(tar.NewReader(counted), board, arch)
	if err != nil {
		return nil, fmt.Errorf("%w: tarball %s: %w", ErrRefused, file, err)
	}
	// the tar reader stops at the end marker; the padding after it is part of what the device hashes
	if _, err := io.Copy(io.Discard, counted); err != nil {
		return nil, err
	}
	b.Path, b.Size, b.Sum = file, counted.n, hex.EncodeToString(whole.Sum(nil))
	return b, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func readBundle(tr *tar.Reader, board, arch string) (*Bundle, error) {
	hdr, err := tr.Next()
	if err != nil || hdr.Name != "MANIFEST" || hdr.Typeflag != tar.TypeReg || hdr.Size > maxRead {
		return nil, errors.New("MANIFEST is not its first entry")
	}
	raw, err := io.ReadAll(tr)
	if err != nil {
		return nil, err
	}
	sums, headers, err := parseManifest(raw)
	if err != nil {
		return nil, err
	}
	if headers["board"] != board || headers["arch"] != arch {
		return nil, fmt.Errorf("built for %s %s, not %s %s", headers["board"], headers["arch"], board, arch)
	}
	if headers["cadence"] == "" {
		return nil, errors.New("MANIFEST names no cadence version")
	}
	withoutRuntimes := headers["runtimes"] == "none"
	if headers["runtimes"] != "" && !withoutRuntimes {
		return nil, errors.New("MANIFEST has an unknown runtimes value")
	}
	b := &Bundle{Version: headers["cadence"], Top: []string{"MANIFEST"}}
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name, err := entryName(hdr.Name)
		if err != nil {
			return nil, err
		}
		top, rest, _ := strings.Cut(name, "/")
		if !bundleTop[top] || top == "MANIFEST" {
			return nil, fmt.Errorf("unexpected entry %s", name)
		}
		if bundleRuntimes[top] && withoutRuntimes {
			return nil, fmt.Errorf("unexpected runtime %s", name)
		}
		if bundleRuntimes[top] && rest != "" && rest != "runtime" && !strings.HasPrefix(rest, "runtime/") {
			return nil, fmt.Errorf("unexpected entry %s", name)
		}
		if rest == "" && !slices.Contains(b.Top, top) {
			b.Top = append(b.Top, top)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return nil, fmt.Errorf("%s is not a regular file or directory", name)
		}
		want, listed := sums[name]
		if !listed || seen[name] {
			return nil, fmt.Errorf("%s is not listed once in MANIFEST", name)
		}
		seen[name] = true
		h := sha256.New()
		var keep bytes.Buffer
		w := io.Writer(h)
		var into *[]byte
		switch name {
		case "share/cadence-run":
			into = &b.Wrapper
		case "share/board-hook":
			into = &b.Hook
		case "share/ca-certificates.crt":
			into = &b.CA
		}
		if into != nil {
			w = io.MultiWriter(h, &keep)
		}
		if _, err := io.Copy(w, tr); err != nil {
			return nil, err
		}
		if hex.EncodeToString(h.Sum(nil)) != want {
			return nil, fmt.Errorf("%s does not match MANIFEST", name)
		}
		if into != nil {
			*into = keep.Bytes()
		}
	}
	for name := range sums {
		if !seen[name] {
			return nil, fmt.Errorf("%s is in MANIFEST but not in the tarball", name)
		}
	}
	for _, name := range required {
		if !seen[name] {
			return nil, fmt.Errorf("%s is missing", name)
		}
	}
	if !withoutRuntimes {
		for top := range bundleRuntimes {
			if !hasPrefix(seen, top+"/runtime/") {
				return nil, fmt.Errorf("%s/runtime is missing", top)
			}
		}
	}
	return b, nil
}

// parseManifest reads MANIFEST's "# <key> <value>" headers and "<sha256>  <path>" lines
func parseManifest(raw []byte) (map[string]string, map[string]string, error) {
	sums, headers := map[string]string{}, map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		if h, ok := strings.CutPrefix(line, "# "); ok {
			k, v, _ := strings.Cut(h, " ")
			headers[k] = v
			continue
		}
		m := manifestLine.FindStringSubmatch(line)
		if m == nil {
			return nil, nil, errors.New("MANIFEST has a line it cannot read")
		}
		name, err := entryName(m[2])
		if err != nil {
			return nil, nil, err
		}
		if _, dup := sums[name]; dup {
			return nil, nil, fmt.Errorf("MANIFEST lists %s twice", name)
		}
		sums[name] = m[1]
	}
	if len(sums) == 0 {
		return nil, nil, errors.New("MANIFEST lists no file")
	}
	return sums, headers, sc.Err()
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9_.+@-]+(/[A-Za-z0-9_.+@-]+)*$`)

// entryName is a tar entry's path without a trailing slash, refused unless it stays inside the tree
func entryName(raw string) (string, error) {
	name := strings.TrimSuffix(raw, "/")
	if !safeName.MatchString(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") ||
		strings.Contains(name, "/../") {
		return "", fmt.Errorf("unsafe entry name %q", raw)
	}
	return name, nil
}

func hasPrefix(set map[string]bool, prefix string) bool {
	for k := range set {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}
