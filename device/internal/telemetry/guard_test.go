package telemetry_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// the pipeline, its sources and the metrics log only to the logger they are handed: slog's default is cadenced's tee,
// and a pipeline that logged through it would feed its own trouble back into itself
func TestPipelineNeverLogsThroughTheDefault(t *testing.T) {
	banned := map[string]bool{
		"Default": true, "SetDefault": true, "Debug": true, "Info": true, "Warn": true, "Error": true, "Log": true,
		"LogAttrs": true, "DebugContext": true, "InfoContext": true, "WarnContext": true, "ErrorContext": true,
	}
	scanned := 0
	for _, dir := range []string{"logs", "metrics", "otlp", "sources", "."} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != dir {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			scanned++
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "slog" && banned[sel.Sel.Name] {
					t.Errorf("%s uses slog.%s", path, sel.Sel.Name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// a walk that silently misses the packages would pass vacuously
	if scanned < 10 {
		t.Fatalf("scanned %d files", scanned)
	}
}
