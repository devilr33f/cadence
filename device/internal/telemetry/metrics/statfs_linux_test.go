package metrics_test

import (
	"path/filepath"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
)

func TestStatfs(t *testing.T) {
	total, free, err := metrics.Statfs(t.TempDir())
	if err != nil || total == 0 || free > total {
		t.Fatalf("total %d, free %d, %v", total, free, err)
	}
	if _, _, err := metrics.Statfs(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("statfs of a missing path")
	}
}
