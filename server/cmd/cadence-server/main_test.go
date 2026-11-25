package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMainDBUnavailable(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cadence-server")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CADENCE_MASTER_KEY_FILE=/unused",
		"DATABASE_URL=postgres://cadence:s3cret-pw@127.0.0.1:1/cadence?connect_timeout=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("err = %v", err)
	}
	s := stderr.String()
	for _, want := range []string{`"msg":"db_unavailable"`, `"stage":"connect"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("stderr lacks %s: %s", want, s)
		}
	}
	for _, bad := range []string{"s3cret-pw", "127.0.0.1"} {
		if strings.Contains(s, bad) {
			t.Fatalf("stderr leaks %s", bad)
		}
	}
}
