package install

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/install/busyboxtest"
)

func TestShellHexReadOnBusybox(t *testing.T) {
	c := busyboxtest.Start(t)
	c.Put("/fake/five", []byte("ab\x00c\n"), "644")
	c.Put("/fake/long", bytes.Repeat([]byte{'x'}, maxRead+10), "644")
	sh := NewShell(box{c}, ShellOptions{Root: "/fake", HexRead: true})
	ctx := context.Background()
	got, ok, err := sh.Read(ctx, "/five")
	if err != nil || !ok || !bytes.Equal(got, []byte("ab\x00c\n")) {
		t.Fatalf("Read = %q, %v, %v", got, ok, err)
	}
	if _, _, err := sh.Read(ctx, "/long"); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a long file: err = %v", err)
	}
	if got, ok, err := sh.Read(ctx, "/missing"); err != nil || ok || got != nil {
		t.Fatalf("a missing file: %q, %v, %v", got, ok, err)
	}
}

// sessionBox is a transport whose login PATH puts a lesser busybox first, as capsula-root's ssh session puts its own
type sessionBox struct{ box }

func (b sessionBox) Run(ctx context.Context, script string) (Result, error) {
	return b.box.Run(ctx, "PATH=/session-bin:$PATH; export PATH; "+script)
}

// with a boot PATH every command finds the stock's applets first and falls through to the session's for the rest:
// the session's sha256sum without -c, dd and hexdump never run, and awk, which the stock lacks, comes from the session
func TestShellBootPathFirstOnBusybox(t *testing.T) {
	c := busyboxtest.Start(t)
	c.StockBin("/stock-bin", busyboxtest.CapsulaBootApplets)
	c.Tree(map[string]string{
		"/session-bin/sha256sum": "#!/bin/sh\nfor a in \"$@\"; do case $a in -c) echo 'sha256sum: unrecognized option: c' >&2; exit 1 ;; esac; done\n" +
			"exec /bin/busybox sha256sum \"$@\"\n",
		"/session-bin/dd":      "#!/bin/sh\necho 'dd: the session one' >&2\nexit 1\n",
		"/session-bin/hexdump": "#!/bin/sh\necho 'hexdump: the session one' >&2\nexit 1\n",
		"/session-bin/awk":     "#!/bin/sh\necho awk >> /session-used\nexec /bin/busybox awk \"$@\"\n",
	}, 0o755)
	c.Tree(map[string]string{
		"/fake/tree/a": "one\n", "/fake/tree/b/c": "two\n",
		"/fake/tree/MANIFEST": "# cadence 1\n" + sum([]byte("one\n")) + "  a\n" + sum([]byte("two\n")) + "  b/c\n",
	}, 0o644)
	c.MustSh("mkdir -p /fake/data/cadence")
	ctx := context.Background()
	s := NewShell(sessionBox{box{c}}, ShellOptions{Root: fakeRoot, DataRoot: "/data/cadence", HexRead: true, BootPath: "/stock-bin"})
	if err := s.Check(ctx, "/tree", "/tree/MANIFEST", "/sums"); err != nil {
		t.Fatalf("a good tree: %v", err)
	}
	if got, ok, err := s.Read(ctx, "/tree/a"); err != nil || !ok || string(got) != "one\n" {
		t.Fatalf("read %q %v %v", got, ok, err)
	}
	if kb, err := s.FreeKB(ctx, "/data/cadence"); err != nil || kb <= 0 {
		t.Fatalf("free %d %v", kb, err)
	}
	if got := string(c.MustSh("cat /session-used")); got != "awk\n" {
		t.Fatalf("the session's awk ran %q", got)
	}
	c.MustSh("echo three > /fake/tree/b/c")
	if err := s.Check(ctx, "/tree", "/tree/MANIFEST", "/sums"); err == nil {
		t.Fatal("a tampered tree passed")
	}

	// without a boot PATH the session's applets win, as on every other board
	if err := NewShell(sessionBox{box{c}}, ShellOptions{Root: fakeRoot}).Check(ctx, "/tree", "/tree/MANIFEST", "/sums"); err == nil {
		t.Fatal("the session's sha256sum checked a tree")
	}
}

func stagedSpeaker(t *testing.T) *fake {
	p := testProfile()
	p.StageDir = "/tmp/cadence-install"
	f := v1SpeakerOn(t, p)
	f.free["/tmp/cadence-install"] = 1 << 20
	return f
}

func TestInstallStagesInStageDir(t *testing.T) {
	f := stagedSpeaker(t)
	mustInstall(t, f)
	ops := f.changes()
	if !slices.Contains(ops, "push /tmp/cadence-install/cadence.tar") || slices.Contains(ops, "push /data/cadence/.install/cadence.tar") {
		t.Fatalf("commands: %q", ops)
	}
	if f.has("/tmp/cadence-install/cadence.tar") {
		t.Fatal("the staged tarball was left behind")
	}
}

func TestStageDirSpaceChecksBothMounts(t *testing.T) {
	for _, mount := range []string{"/tmp/cadence-install", "/data"} {
		f := stagedSpeaker(t)
		f.mkdirAll("/tmp/cadence-install")
		f.free[mount] = 1
		_, err := install(t, f, runOpts(t, f))
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), mount+" has 1 KiB free") {
			t.Fatalf("%s: err = %v", mount, err)
		}
	}
}

func TestStagedTarballThatArrivesDifferentIsRemoved(t *testing.T) {
	f := stagedSpeaker(t)
	f.pushed = func(b []byte) []byte { return append(b, 0) }
	_, err := install(t, f, runOpts(t, f))
	if !errors.Is(err, ErrRefused) || f.has("/tmp/cadence-install/cadence.tar") {
		t.Fatalf("err = %v, tarball left: %v", err, f.has("/tmp/cadence-install/cadence.tar"))
	}
}

func TestPreflightRefusesWhileUpdateFlagExists(t *testing.T) {
	p := testProfile()
	p.RefuseIfPresent = []Refusal{{"/data/update_successful", "the stock selfcheck reboots once voice-agent is missing"}}
	f := v1SpeakerOn(t, p)
	f.put("/data/update_successful", nil, 0o644)
	out, err := install(t, f, runOpts(t, f))
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "the stock selfcheck reboots once voice-agent is missing") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if len(f.changes()) != 0 {
		t.Fatalf("a refused run changed: %q", f.changes())
	}
}
