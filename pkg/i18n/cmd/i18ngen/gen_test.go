package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const root = "../../../.."

// the committed output matches the catalogs: run make i18n after changing i18n/
func TestGeneratedUpToDate(t *testing.T) {
	files, err := Generate(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Path)))
		if err != nil {
			t.Errorf("%s: %v (run make i18n)", f.Path, err)
			continue
		}
		if !bytes.Equal(got, f.Data) {
			t.Errorf("%s drifted from i18n/ (run make i18n)", f.Path)
		}
	}
}

func TestCppString(t *testing.T) {
	for in, want := range map[string]string{
		`a"b\c`: `"a\"b\\c"`, "Да1": `"\320\224\320\2601"`, "a?": `"a\?"`, "x\ny": `"x\012y"`,
	} {
		if got := cppString(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func TestCamel(t *testing.T) {
	if got := camel("player.osd.skip_opening"); got != "PlayerOsdSkipOpening" {
		t.Error(got)
	}
}
