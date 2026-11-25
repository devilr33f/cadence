package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAskpassModePrintsTheFirstLine(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pw-file")
	if err := os.WriteFile(file, []byte("secret\r\nsecond"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := askpass(file, &out, &errOut); code != 0 || out.String() != "secret" || errOut.Len() != 0 {
		t.Fatalf("status %d, out %q, err %q", code, out.String(), errOut.String())
	}

	missing := filepath.Join(t.TempDir(), "absent-file")
	out.Reset()
	if code := askpass(missing, &out, &errOut); code != 1 || out.Len() != 0 {
		t.Fatalf("status %d, out %q", code, out.String())
	}
	if e := errOut.String(); e == "" || strings.Contains(e, "absent-file") || strings.Contains(e, "secret") {
		t.Fatalf("stderr %q", e)
	}
}

func TestPasswordFileFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--ssh", "root@192.0.2.1", "--key", "k", "--password-file", "p", "--tarball", "x.tar"},
		{"--ssh", "root@192.0.2.1", "--tarball", "x.tar"},
		{"--adb", "192.0.2.1:5555", "--password-file", "p", "--tarball", "x.tar"},
	} {
		if code, _, errOut := runWith(t, args...); code != exitUsage || !strings.Contains(errOut, "usage") {
			t.Errorf("%q: status %d, %q", args, code, errOut)
		}
	}
	if code, _, errOut := runWith(t, "--ssh", "root@192.0.2.1", "--password-file", "p", "--tarball", "x.tar"); code == exitUsage {
		t.Errorf("--password-file alone is a usage error: %s", errOut)
	}
}
