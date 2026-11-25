package updater_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
)

const day = 24 * time.Hour

// 2026-10-28: a set clock
var now = time.Unix(1_793_100_000, 0)

// left is the epoch of a build with days left before it expires
func left(days int) int64 { return now.Add(time.Duration(days)*day - updater.Lifetime).Unix() }

// elf is a file that reads as a 64-bit little-endian build for machine, and
// tells the fake prober its epoch
func elf(machine uint16, epoch int64) []byte {
	head := make([]byte, 64)
	copy(head, "\x7fELF")
	head[4], head[5], head[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(head[16:], 2)
	binary.LittleEndian.PutUint16(head[18:], machine)
	body := fmt.Appendf(head, "epoch=%d;", epoch)
	return append(body, bytes.Repeat([]byte{0x5a}, 3000)...)
}

func arm(epoch int64) []byte { return elf(updater.MachineAArch64, epoch) }

type entry struct {
	name string
	typ  byte
	body []byte
	link string
}

func file(name string, body []byte) entry { return entry{name: name, typ: tar.TypeReg, body: body} }

func tarball(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o755, Linkname: e.link}
		if e.typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.body[:h.Size]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gz(t *testing.T, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// release is spotify's tarball: notes, the build and its licenses
func release(t *testing.T, build []byte) []byte {
	t.Helper()
	return gz(t, tarball(t, file("CHANGELOG.md", []byte("notes")), file("soloist", build), file("THIRD_PARTY_LICENSES.txt", []byte("licenses"))))
}

// fakeFetch serves body, or fails with err, or cuts the body with a network error
type fakeFetch struct {
	body  []byte
	err   error
	cut   bool
	calls atomic.Int32
}

func (f *fakeFetch) Get(context.Context) (io.ReadCloser, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	if f.cut {
		return io.NopCloser(io.MultiReader(bytes.NewReader(f.body), errReader{})), nil
	}
	return io.NopCloser(bytes.NewReader(f.body)), nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

var epochIn = regexp.MustCompile(`epoch=(\d+);`)

// fakeProber reads a build as audiod's probe would: what elf wrote in it, or
// fails for every path with fail, or for the file named in failFor
type fakeProber struct {
	mu      sync.Mutex
	fail    error
	failFor map[string]error
	paths   []string
}

func (p *fakeProber) Probe(_ context.Context, path string) (updater.Build, error) {
	p.mu.Lock()
	p.paths = append(p.paths, filepath.Base(path))
	fail := p.fail
	if f, ok := p.failFor[filepath.Base(path)]; ok {
		fail = f
	}
	p.mu.Unlock()
	if fail != nil {
		return updater.Build{}, fail
	}
	raw, err := os.ReadFile(path) //nolint:gosec // test dir
	if err != nil {
		return updater.Build{}, updater.FailIO
	}
	m := epochIn.FindSubmatch(raw)
	if m == nil {
		return updater.Build{}, updater.FailVersion
	}
	epoch, _ := strconv.ParseInt(string(m[1]), 10, 64)
	return updater.Build{Version: "1.3.10.18", Epoch: epoch}, nil
}

func (p *fakeProber) probed() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.paths)
}

// files lists dir's names, sorted
func files(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range des {
		out = append(out, d.Name())
	}
	return out
}

func install(t *testing.T, dir string, epoch int64) {
	t.Helper()
	write(t, filepath.Join(dir, "soloist"), arm(epoch))
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o755); err != nil { //nolint:gosec // a test build is executable
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test dir
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newUpdater(dir string, fetch updater.Fetcher, probe updater.Prober) *updater.Updater {
	return &updater.Updater{Dir: dir, Fetch: fetch, Probe: probe, Machine: updater.MachineAArch64, Now: func() time.Time { return now }}
}
