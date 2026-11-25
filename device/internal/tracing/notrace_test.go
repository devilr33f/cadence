package tracing_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// cadence_notrace builds cadenced without the otel sdk; the default build links it
func TestNotraceBuildLinksNoSDK(t *testing.T) {
	if testing.Short() {
		t.Skip("lists cadenced's dependencies twice")
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		tags string
		want bool
	}{{"", true}, {"cadence_notrace", false}} {
		cmd := exec.Command(gobin, "list", "-deps", "-tags", c.tags, "./cmd/cadenced") //nolint:gosec // the go tool with fixed arguments
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -tags %q: %v", c.tags, err)
		}
		deps := strings.Fields(string(out))
		linked := slices.ContainsFunc(deps, func(d string) bool { return strings.HasPrefix(d, "go.opentelemetry.io/otel/sdk") })
		if linked != c.want || !slices.Contains(deps, "git.shino.boo/devilreef/cadence/device/internal/tracing") {
			t.Fatalf("tags %q: sdk linked %v, want %v", c.tags, linked, c.want)
		}
	}
}
