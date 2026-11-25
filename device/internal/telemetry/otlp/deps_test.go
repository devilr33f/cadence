package otlp_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// the otlp messages are generated without grpc stubs: neither binary may link grpc
func TestBinariesLinkNoGRPC(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the server's dependencies too; make test runs it")
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// as the binaries are built: build tags pick files per target, whatever the host
	for _, b := range []struct {
		module, pkg, needs string
		env                []string
	}{
		// cadenced ships logs, so the list covers the generated otlp code
		{"device", "./cmd/cadenced", "git.shino.boo/devilreef/cadence/pkg/gen/otlp/logs/v1", []string{"GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0"}},
		{"server", "./cmd/cadence-server", "", []string{"GOOS=linux"}},
	} {
		cmd := exec.Command(gobin, "list", "-deps", b.pkg) //nolint:gosec // the go tool with fixed arguments
		cmd.Dir = filepath.Join(root, b.module)
		cmd.Env = append(os.Environ(), b.env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list %s: %v", b.pkg, err)
		}
		deps := strings.Fields(string(out))
		if len(deps) == 0 || b.needs != "" && !slices.Contains(deps, b.needs) {
			t.Fatalf("go list %s listed %d packages, not %s", b.pkg, len(deps), b.needs)
		}
		for _, d := range deps {
			if d == "google.golang.org/grpc" || strings.HasPrefix(d, "google.golang.org/grpc/") {
				t.Fatalf("%s links %s", b.pkg, d)
			}
		}
	}
}
