package audiod_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audiod"
)

// a session's first packet, while the vendor status is pending, waits out both of audiod's probe children (the
// engines', then the command model's), each at audiod's own limit, and the engines' start after them
func TestFirstPacketWaitCoversBothVendorProbes(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "media", "audiod", "vendor-probe.h"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`kProbeLimit = std::chrono::seconds\((\d+)\);`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no kProbeLimit in vendor-probe.h")
	}
	n, _ := strconv.Atoi(string(m[1]))
	probe := time.Duration(n) * time.Second
	if audiod.VendorProbeLimit != probe {
		t.Fatalf("cadenced takes audiod's probe limit for %v, vendor-probe.h says %v", audiod.VendorProbeLimit, probe)
	}
	// at least this long for the engines to start once both probes passed
	const margin = 2 * time.Second
	if got := audiod.DefaultTimings.FirstPacketVendor; got < 2*probe+margin {
		t.Fatalf("the pending wait %v ends before two probes at %v each and the engines' start", got, probe)
	}
}
