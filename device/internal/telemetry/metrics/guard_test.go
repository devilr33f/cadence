package metrics_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// rule 8: process telemetry never names a command line or an environment, so no path to one can be built from any
// source of the telemetry packages (the wrapped filesystem of the tests checks the paths the collector opens)
func TestSourcesNameNoCommandLine(t *testing.T) {
	scanned := 0
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, _ := strconv.Unquote(lit.Value)
			for _, banned := range []string{"cmdline", "environ"} {
				if strings.Contains(strings.ToLower(v), banned) {
					t.Errorf("%s names %q: %q", path, banned, v)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// a walk that silently misses the packages would pass vacuously
	if scanned < 20 {
		t.Fatalf("scanned %d files", scanned)
	}
}
