package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// the subcommand needs no database and no config: the same bytes every time,
// on stdout or in the file given
func TestAdminOpenAPIRunsWithoutConfig(t *testing.T) {
	run := func(args ...string) ([]byte, string) {
		var out, errb bytes.Buffer
		if code := runAdmin(args, func(string) string { return "" }, strings.NewReader(""), &out, &errb); code != 0 {
			t.Fatalf("%v: code %d: %s", args, code, errb.String())
		}
		return out.Bytes(), errb.String()
	}
	first, errText := run("openapi")
	second, _ := run("openapi")
	if errText != "" || len(first) == 0 || !bytes.Equal(first, second) {
		t.Fatalf("stdout %d bytes, again %d, stderr %q", len(first), len(second), errText)
	}
	path := filepath.Join(t.TempDir(), "o.json")
	if stdout, _ := run("openapi", "-o", path); len(stdout) != 0 {
		t.Fatal("stdout is not empty with -o")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, first) {
		t.Fatalf("file: %v, %d bytes", err, len(got))
	}
	var out, errb bytes.Buffer
	if code := runAdmin([]string{"openapi", "-x"}, func(string) string { return "" }, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("bad flag: code %d", code)
	}
}
