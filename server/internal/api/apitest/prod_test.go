package apitest_test

import (
	"os/exec"
	"strings"
	"testing"
)

// Verify takes any bearer as an admin token: it must never reach the server
func TestNotInTheServer(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "git.shino.boo/devilreef/cadence/server/cmd/cadence-server").Output()
	if err != nil {
		t.Fatal(err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 50 {
		t.Fatalf("go list gave %d packages", len(deps))
	}
	for _, d := range deps {
		if strings.HasSuffix(d, "/internal/api/apitest") {
			t.Fatalf("cadence-server imports %s", d)
		}
	}
}
