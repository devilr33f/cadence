package updater_test

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
)

const mib = 1 << 20

// only a regular entry named exactly soloist or ./soloist is taken, the first
// one (v1's rule); links, directories and any other name are skipped
func TestExtractTakesTheFirstTopLevelRegularSoloist(t *testing.T) {
	a, b := arm(1), arm(2)
	for _, c := range []struct {
		name    string
		entries []entry
		want    []byte
	}{
		{"among notes", []entry{file("CHANGELOG.md", []byte("x")), file("soloist", a), file("LICENSES", []byte("y"))}, a},
		{"dot slash", []entry{file("./soloist", a)}, a},
		{"the first of two", []entry{file("soloist", a), file("./soloist", b)}, a},
		{"after a symlink", []entry{{name: "soloist", typ: tar.TypeSymlink, link: "/bin/sh"}, file("soloist", b)}, b},
		{"after a hard link", []entry{file("x", a), {name: "soloist", typ: tar.TypeLink, link: "x"}, file("./soloist", b)}, b},
		{"after a directory", []entry{{name: "soloist", typ: tar.TypeDir}, file("soloist", b)}, b},
		{"after a nested one", []entry{file("bin/soloist", a), file("../soloist", a), file("soloist", b)}, b},
	} {
		dest := filepath.Join(t.TempDir(), "soloist.next")
		if err := updater.Extract(bytes.NewReader(gz(t, tarball(t, c.entries...))), dest); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := read(t, dest); !bytes.Equal(got, c.want) {
			t.Fatalf("%s: took the wrong entry", c.name)
		}
		if st, _ := os.Stat(dest); runtime.GOOS != "windows" && st.Mode().Perm() != 0o755 {
			t.Fatalf("%s: mode %v", c.name, st.Mode())
		}
	}
}

// review focus 4: nothing outside the one top-level name is ever written
func TestExtractRefusesAnyOtherSoloist(t *testing.T) {
	a := arm(1)
	for _, c := range []struct {
		name    string
		entries []entry
	}{
		{"none", []entry{file("CHANGELOG.md", []byte("x"))}},
		{"parent", []entry{file("../soloist", a)}},
		{"absolute", []entry{file("/soloist", a)}},
		{"through a parent", []entry{file("x/../soloist", a)}},
		{"nested", []entry{file("bin/soloist", a)}},
		{"dot dot slash", []entry{file("././soloist", a)}},
		{"a directory", []entry{{name: "soloist/", typ: tar.TypeDir}, file("soloist/x", a)}},
		{"a symlink", []entry{{name: "soloist", typ: tar.TypeSymlink, link: "/etc/passwd"}}},
		{"a hard link", []entry{file("x", a), {name: "soloist", typ: tar.TypeLink, link: "x"}}},
	} {
		dir := t.TempDir()
		dest := filepath.Join(dir, "soloist.next")
		if err := updater.Extract(bytes.NewReader(gz(t, tarball(t, c.entries...))), dest); !errors.Is(err, updater.FailFormat) {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := files(t, dir); len(got) != 0 {
			t.Fatalf("%s: left %v", c.name, got)
		}
	}
}

func TestExtractSizeLimits(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soloist.next")
	// a header that says one byte over 128 MiB is refused before its body is read
	var raw bytes.Buffer
	w := tar.NewWriter(&raw)
	if err := w.WriteHeader(&tar.Header{Name: "soloist", Typeflag: tar.TypeReg, Mode: 0o755, Size: 128*mib + 1}); err != nil {
		t.Fatal(err)
	}
	if err := updater.Extract(bytes.NewReader(gz(t, raw.Bytes())), dest); !errors.Is(err, updater.FailSize) {
		t.Fatalf("oversize: %v", err)
	}
	// a binary at the limit is taken; the inflated stream past its cap is size
	body := gz(t, tarball(t, file("soloist", arm(1)), file("padding", make([]byte, 64*1024))))
	if err := updater.ExtractLimited(bytes.NewReader(body), dest, int64(len(arm(1))), 1*mib); err != nil {
		t.Fatalf("at the binary limit: %v", err)
	}
	if err := updater.ExtractLimited(bytes.NewReader(body), dest, int64(len(arm(1)))-1, 1*mib); !errors.Is(err, updater.FailSize) {
		t.Fatalf("one byte over the binary limit: %v", err)
	}
	if err := updater.ExtractLimited(bytes.NewReader(body), dest, 1*mib, 32*1024); !errors.Is(err, updater.FailSize) {
		t.Fatalf("past the inflated cap: %v", err)
	}
	if got := files(t, dir); len(got) != 0 {
		t.Fatalf("left %v", got)
	}
}

// the rest of the stream is read too, so gzip's own length and checksum count
func TestExtractFormatFailures(t *testing.T) {
	whole := gz(t, tarball(t, file("soloist", arm(1)), file("LICENSES", bytes.Repeat([]byte("l"), 5000))))
	crc := append([]byte(nil), whole...)
	crc[len(crc)-8] ^= 0xff
	length := append([]byte(nil), whole...)
	length[len(length)-1] ^= 0xff
	for _, c := range []struct {
		name string
		body []byte
	}{
		{"not gzip", []byte("<html>moved</html>")},
		{"plain tar", tarball(t, file("soloist", arm(1)))},
		{"cut", whole[:len(whole)/2]},
		{"cut in the trailer", whole[:len(whole)-4]},
		{"bad crc", crc},
		{"bad length", length},
		{"empty", nil},
	} {
		dir := t.TempDir()
		if err := updater.Extract(bytes.NewReader(c.body), filepath.Join(dir, "soloist.next")); !errors.Is(err, updater.FailFormat) {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := files(t, dir); len(got) != 0 {
			t.Fatalf("%s: left %v", c.name, got)
		}
	}
	// one gzip member, as v1 read it: what follows it is never read
	if err := updater.Extract(io.MultiReader(bytes.NewReader(whole), bytes.NewReader([]byte("trailing"))), filepath.Join(t.TempDir(), "n")); err != nil {
		t.Fatalf("bytes after the member: %v", err)
	}
}

// an error from the body itself is the network's, wherever it cuts the stream
func TestExtractBodyErrorIsHTTP(t *testing.T) {
	whole := gz(t, tarball(t, file("soloist", bytes.Repeat(arm(1), 30)), file("LICENSES", []byte("l"))))
	for _, at := range []int{0, 10, len(whole) / 2, len(whole) - 2} {
		dir := t.TempDir()
		body := io.MultiReader(bytes.NewReader(whole[:at]), errReader{})
		if err := updater.Extract(body, filepath.Join(dir, "soloist.next")); !errors.Is(err, updater.FailHTTP) {
			t.Fatalf("cut at %d: %v", at, err)
		}
		if got := files(t, dir); len(got) != 0 {
			t.Fatalf("cut at %d: left %v", at, got)
		}
	}
}

// whatever is at soloist.next goes first, and a link there is never followed
func TestExtractReplacesWhatIsAtNext(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soloist.next")
	outside := filepath.Join(t.TempDir(), "elsewhere")
	write(t, outside, []byte("keep"))
	if err := os.Symlink(outside, dest); err != nil {
		t.Skip("no symlinks here")
	}
	if err := updater.Extract(bytes.NewReader(release(t, arm(1))), dest); err != nil {
		t.Fatal(err)
	}
	if got := read(t, outside); string(got) != "keep" {
		t.Fatalf("followed the link: %q", got)
	}
	if st, err := os.Lstat(dest); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("next: %v, %v", st, err)
	}
}

// a write the disk refuses is io, and leaves no file behind
func TestExtractWriteFailureIsIO(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	write(t, blocker, []byte("a file"))
	if err := updater.Extract(bytes.NewReader(release(t, arm(1))), filepath.Join(blocker, "soloist.next")); !errors.Is(err, updater.FailIO) {
		t.Fatalf("err = %v", err)
	}
	if got := read(t, blocker); string(got) != "a file" {
		t.Fatalf("blocker = %q", got)
	}
}
