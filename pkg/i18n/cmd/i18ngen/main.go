// Command i18ngen checks the catalogs in i18n/ and writes what is made from them: the server's typed texts
// (pkg/i18n/servertext) with its embedded catalog copy, and the screen's key header and built-in catalog
// (screen/i18n/gen), so a key the screen names that no catalog has fails its build. Run from the repo root (make
// i18n); its test fails while the committed output drifts.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", ".", "the repo root")
	flag.Parse()
	files, err := Generate(os.DirFS(*root))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, f := range files {
		p := filepath.Join(*root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // source directories
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil { //nolint:gosec // source files, world-readable
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
