package main

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nothing here reaches a device: the addresses are TEST-NET's and the transports' programs do not exist
func runWith(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	sshBin, adbBin = filepath.Join(t.TempDir(), "no-ssh"), filepath.Join(t.TempDir(), "no-adb")
	var out, errOut bytes.Buffer
	code := run(t.Context(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--tarball", "x.tar", "--server", "https://h", "--code", "c"},
		{"--ssh", "root@192.0.2.1", "--tarball", "x.tar"},
		{"--ssh", "root@192.0.2.1", "--key", "k"},
		{"--adb", "192.0.2.1:5555", "--to-v1", "--tarball", "x.tar"},
		{"--adb", "192.0.2.1:5555", "--to-v1", "--code", "c"},
		{"--adb", "192.0.2.1:5555", "--tarball", "x.tar", "extra"},
	} {
		if code, _, errOut := runWith(t, args...); code != exitUsage || !strings.Contains(errOut, "usage") {
			t.Errorf("%q: status %d, %q", args, code, errOut)
		}
	}
}

// a tarball that is not a pickle build is refused before any transport is tried
func TestTarballCheckedFirst(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.tar")
	if err := os.WriteFile(bad, []byte("not a tarball"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runWith(t, "--adb", "192.0.2.1:5555", "--tarball", bad, "--server", "https://h", "--code", "SECRET-CODE")
	if code != exitRefused || !strings.Contains(errOut, "MANIFEST is not its first entry") || strings.Contains(errOut, "adb") {
		t.Fatalf("status %d\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(out+errOut, "SECRET-CODE") {
		t.Fatal("the code was printed")
	}
}

func TestNoTransportAnswers(t *testing.T) {
	code, _, errOut := runWith(t, "--ssh", "root@192.0.2.1", "--key", "k", "--adb", "192.0.2.1:5555", "--to-v1")
	if code != exitFailed || !strings.Contains(errOut, "ssh root@192.0.2.1:") || !strings.Contains(errOut, "adb 192.0.2.1:5555:") ||
		!strings.Contains(errOut, "no transport answers") {
		t.Fatalf("status %d: %s", code, errOut)
	}
}

func TestUsageOfTheBoardsFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--adb", "192.0.2.1:5555", "--board", "yandex-mini", "--to-v1"},
		{"--adb", "192.0.2.1:5555", "--board", "yandex-midi", "--to-v1", "--restore-stock", "--ota-blocked"},
		{"--adb", "192.0.2.1:5555", "--board", "yandex-midi", "--restore-stock", "--ota-blocked", "--tarball", "x.tar"},
		{"--adb", "192.0.2.1:5555", "--board", "yandex-midi", "--restore-stock", "--ota-blocked", "--code", "c"},
		{"--adb", "192.0.2.1:5555", "--board", "yandex-midi", "--restore-stock", "--ota-blocked", "--accept-firmware"},
	} {
		if code, _, errOut := runWith(t, args...); code != exitUsage || !strings.Contains(errOut, "usage") {
			t.Errorf("%q: status %d, %q", args, code, errOut)
		}
	}
}

// the tarball must be built for the board named (pickle when none is)
func TestTarballOfAnotherBoardIsRefused(t *testing.T) {
	tarball := filepath.Join(t.TempDir(), "cadence-x-yandex-pickle-arm64.tar")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	manifest := "# cadence x\n# board yandex-pickle\n# arch arm64\n" + strings.Repeat("0", 64) + "  bin/cadenced\n"
	if err := tw.WriteHeader(&tar.Header{Name: "MANIFEST", Mode: 0o644, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tarball, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runWith(t, "--adb", "192.0.2.1:5555", "--board", "yandex-midi", "--tarball", tarball, "--server", "https://h", "--ota-blocked")
	if code != exitRefused || !strings.Contains(errOut, "built for yandex-pickle arm64, not yandex-midi armhf") {
		t.Fatalf("status %d: %s", code, errOut)
	}
}

// --restore-stock reaches the device (none answers here) only with a board that keeps its stock named
func TestRestoreStockNeedsATransport(t *testing.T) {
	code, _, errOut := runWith(t, "--adb", "192.0.2.1:5555", "--board", "yandex-midi", "--restore-stock", "--ota-blocked")
	if code != exitFailed || !strings.Contains(errOut, "no transport answers") {
		t.Fatalf("status %d: %s", code, errOut)
	}
}

// after a failed install the way back is the board's own: v1 on the yandex boards, their stock with the router's OTA
// block where a board keeps one, and only the stock on an overlay board, which never ran v1
func TestFailHintNamesTheBoardsWayBack(t *testing.T) {
	for board, want := range map[string]string{
		"yandex-pickle":   "run it again to resume, or with --to-v1 to go back to v1",
		"yandex-midi":     "run it again to resume, or with --to-v1 to go back to v1, or with --restore-stock --ota-blocked to give it back to its stock",
		"yandex-micro":    "run it again to resume, or with --to-v1 to go back to v1, or with --restore-stock --ota-blocked to give it back to its stock",
		"vk-capsula-mini": "run it again to resume, or with --restore-stock to give it back to its stock",
	} {
		p, ok := profiles[board]
		if !ok {
			t.Fatalf("no board %s", board)
		}
		if got := failHint(p()); got != want {
			t.Errorf("%s: %q, want %q", board, got, want)
		}
	}
}

// the vk capsula is a board cadence-install knows: its usage names it, and its --restore-stock reaches for a transport
// without --ota-blocked
func TestVKCapsulaIsKnown(t *testing.T) {
	code, _, errOut := runWith(t, "--ssh", "root@192.0.2.1", "--key", "k", "--board", "vk-capsula-mini", "--restore-stock")
	if code != exitFailed || !strings.Contains(errOut, "no transport answers") {
		t.Fatalf("status %d: %s", code, errOut)
	}
	if !strings.Contains(usage, "vk-capsula-mini") {
		t.Fatalf("usage:\n%s", usage)
	}
}

// --ca-file is read and checked before the tarball and any transport; the way back takes none
func TestCAFileCheckedFirst(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("-----BEGIN PRIVATE KEY-----\nc2VjcmV0\n-----END PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ca := range []string{junk, filepath.Join(dir, "missing.pem")} {
		code, out, errOut := runWith(t, "--adb", "192.0.2.1:5555", "--tarball", filepath.Join(dir, "x.tar"), "--server", "https://h",
			"--code", "c", "--ca-file", ca)
		if code != exitRefused || !strings.Contains(errOut, "--ca-file") || strings.Contains(errOut, "MANIFEST") ||
			strings.Contains(errOut, "adb") || strings.Contains(out+errOut, "c2VjcmV0") {
			t.Errorf("%s: status %d\n%s\n%s", ca, code, out, errOut)
		}
	}
	if code, _, errOut := runWith(t, "--adb", "192.0.2.1:5555", "--to-v1", "--ca-file", junk); code != exitUsage || !strings.Contains(errOut, "usage") {
		t.Errorf("--to-v1 --ca-file: status %d, %q", code, errOut)
	}
}
