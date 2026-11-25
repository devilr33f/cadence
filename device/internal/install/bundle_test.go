package install

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestBundleOpens(t *testing.T) {
	file := writeBundle(t, bundleFiles(), bundleHeaders, nil)
	b, err := OpenBundle(file, "yandex-pickle", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file)
	s := sha256.Sum256(raw)
	if b.Sum != hex.EncodeToString(s[:]) || b.Size != int64(len(raw)) || b.Version != "72407e22cafd" ||
		string(b.Wrapper) != "#!/bin/sh\n# the wrapper\n" || string(b.Hook) != "# the board's hook\n" {
		t.Fatalf("bundle = %+v", b)
	}
	if want := []string{"MANIFEST", "bin", "share", "licenses", "soloist", "airplay"}; !slices.Equal(b.Top, want) {
		t.Fatalf("top = %q", b.Top)
	}
}

func TestBundleRefuses(t *testing.T) {
	without := func(name string) []bundleFile {
		return slices.DeleteFunc(bundleFiles(), func(f bundleFile) bool { return f.name == name })
	}
	with := func(extra ...bundleFile) []bundleFile { return append(bundleFiles(), extra...) }
	for name, tc := range map[string]struct {
		files    []bundleFile
		headers  []string
		manifest func(string) string
		want     string
	}{
		"other board":   {headers: []string{"cadence x", "board yandex-midi", "arch armhf"}, want: "built for yandex-midi armhf"},
		"no version":    {headers: []string{"board yandex-pickle", "arch arm64"}, want: "no cadence version"},
		"tampered file": {manifest: func(m string) string { return flipHash(m, "bin/cadenced") }, want: "bin/cadenced does not match"},
		"unlisted file": {manifest: func(m string) string { return dropLine(m, "licenses/README") }, want: "licenses/README is not listed once"},
		"listed, absent": {
			manifest: func(m string) string { return m + strings.Repeat("0", 64) + "  share/extra\n" }, want: "share/extra is in MANIFEST but not in the tarball",
		},
		"bad line":      {manifest: func(m string) string { return m + "garbage\n" }, want: "a line it cannot read"},
		"symlink":       {files: with(bundleFile{name: "bin/sh", data: "/bin/busybox", kind: tar.TypeSymlink}), want: "bin/sh is not a regular file"},
		"escape":        {files: with(bundleFile{name: "bin/../../etc/inittab", data: "x"}), want: "unsafe entry name"},
		"absolute":      {files: with(bundleFile{name: "/etc/inittab", data: "x"}), want: "unsafe entry name"},
		"other top":     {files: with(bundleFile{name: "etc/inittab", data: "x"}), want: "unexpected entry etc/inittab"},
		"runtime only":  {files: with(bundleFile{name: "soloist/soloist", data: "x"}), want: "unexpected entry soloist/soloist"},
		"no wrapper":    {files: without("share/cadence-run"), want: "share/cadence-run is missing"},
		"no hook":       {files: without("share/board-hook"), want: "share/board-hook is missing"},
		"no airplay":    {files: without("airplay/runtime/bin/shairport-sync"), want: "airplay/runtime is missing"},
		"no audiod":     {files: without("bin/cadence-audiod"), want: "bin/cadence-audiod is missing"},
		"manifest late": {files: with(bundleFile{name: "MANIFEST", data: "x"}), want: "unexpected entry MANIFEST"},
	} {
		t.Run(name, func(t *testing.T) {
			files, headers := tc.files, tc.headers
			if files == nil {
				files = bundleFiles()
			}
			if headers == nil {
				headers = bundleHeaders
			}
			_, err := OpenBundle(writeBundle(t, files, headers, tc.manifest), "yandex-pickle", "arm64")
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBundleExplicitlyWithoutRuntimes(t *testing.T) {
	files := slices.DeleteFunc(bundleFiles(), func(f bundleFile) bool {
		return strings.HasPrefix(f.name, "soloist/") || strings.HasPrefix(f.name, "airplay/")
	})
	headers := append(slices.Clone(bundleHeaders), "runtimes none")
	b, err := OpenBundle(writeBundle(t, files, headers, nil), "yandex-pickle", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(b.Top, "soloist") || slices.Contains(b.Top, "airplay") {
		t.Fatalf("top = %q", b.Top)
	}
	if _, err := OpenBundle(writeBundle(t, bundleFiles(), headers, nil), "yandex-pickle", "arm64"); err == nil ||
		!strings.Contains(err.Error(), "unexpected runtime") {
		t.Fatalf("runtime accepted in a runtime-free bundle: %v", err)
	}
}

func dropLine(text, suffix string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasSuffix(l, "  "+suffix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// the tarball pack-device.sh writes for a real build, when one is there: make device-build BOARD=yandex-pickle
func TestBuiltBundle(t *testing.T) {
	file := os.Getenv("CADENCE_DEVICE_TARBALL")
	if file == "" {
		t.Skip("CADENCE_DEVICE_TARBALL names no tarball")
	}
	b, err := OpenBundle(file, "yandex-pickle", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cadence %s, %d bytes, sha256 %s, top %q", b.Version, b.Size, b.Sum, b.Top)
	// and busybox unpacks and checks it as the speaker will
	_, s := v1Box(t)
	ctx := context.Background()
	for _, err := range []error{
		s.Mkdir(ctx, "/data/cadence/.install", 0o700),
		s.Push(ctx, file, "/data/cadence/.install/cadence.tar"),
		s.Unpack(ctx, "/data/cadence/.install/cadence.tar", "/data/cadence/.install/tree"),
		s.Check(ctx, "/data/cadence/.install/tree", "/data/cadence/.install/tree/MANIFEST", "/data/cadence/.install/sums"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, _, _ := s.Sum(ctx, "/data/cadence/.install/cadence.tar"); got != b.Sum {
		t.Fatalf("pushed sum %s", got)
	}
}

// flipHash changes the first digit of name's sha256 in MANIFEST
func flipHash(text, name string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.HasSuffix(l, "  "+name) {
			d := "0"
			if l[0] == '0' {
				d = "1"
			}
			lines[i] = d + l[1:]
		}
	}
	return strings.Join(lines, "\n")
}
