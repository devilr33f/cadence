package install

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/install/busyboxtest"
)

// an upgrade over an installed tree on a board with a stage dir: the new tree needs its room beside the old one at
// stage's peak, and the spare only once place removed the old one, so the old tree's room counts toward the spare;
// a first install keeps the rule as it was, and v1's tree, which stage moves aside, counts for nothing
func TestStageDirSpaceCountsTheInstalledTree(t *testing.T) {
	const spare = 100
	for name, tc := range map[string]struct {
		// v1-moved recorded, the KiB of the tree in place, and /data's free KiB past the tarball's
		moved     bool
		old, free int64
		refused   bool
	}{
		"first install, the tree and the spare fit":   {false, 0, spare, false},
		"first install, short of the spare":           {false, 0, spare - 1, true},
		"v1's tree is moved aside, never replaced":    {false, 60, spare - 60, true},
		"upgrade, the old tree makes up the spare":    {true, 60, spare - 60, false},
		"upgrade, the old tree falls short":           {true, 60, spare - 61, true},
		"upgrade, no room for the new tree beside it": {true, 10 * spare, -1, true},
	} {
		t.Run(name, func(t *testing.T) {
			f := stagedSpeaker(t)
			f.p.SpareKB = spare
			if tc.moved {
				f.put("/data/cadence/install.state", []byte("v1-moved\n"), 0o600)
			}
			if tc.old > 0 {
				third := tc.old / 3
				f.put("/data/cadence/bin/cadenced", make([]byte, third*1024), 0o755)
				f.put("/data/cadence/share/ca-certificates.crt", make([]byte, third*1024), 0o644)
				f.put("/data/cadence/soloist/runtime/lib/ld.so", make([]byte, (tc.old-2*third)*1024), 0o755)
			}
			// what no stage replaces: cadence's own state, and soloist's download beside its runtime
			f.put("/data/cadence/log/cadenced.log", make([]byte, 500*1024), 0o644)
			f.put("/data/cadence/soloist/soloist", make([]byte, 500*1024), 0o755)
			o := runOpts(t, f)
			tarKB := o.Bundle.Size / 1024
			f.free["/data"] = tarKB + tc.free
			in := New(f, f.p, o)
			if err := in.loadState(context.Background()); err != nil {
				t.Fatal(err)
			}
			err := in.space(context.Background())
			if tc.refused != (err != nil) {
				t.Fatalf("tarball %d KiB, free %d KiB: err = %v", tarKB, f.free["/data"], err)
			}
			if err != nil && (!errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "/data has")) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestUsedKBCountsWhatIsThere(t *testing.T) {
	c := busyboxtest.Start(t)
	c.MustSh("mkdir -p /fake/data/cadence/bin /fake/data/cadence/share && " +
		"dd if=/dev/zero of=/fake/data/cadence/bin/a bs=1024 count=300 2>/dev/null && " +
		"dd if=/dev/zero of=/fake/data/cadence/share/b bs=1024 count=200 2>/dev/null")
	sh := NewShell(box{c}, ShellOptions{Root: "/fake"})
	ctx := context.Background()
	kb, err := sh.UsedKB(ctx, "/data/cadence/bin", "/data/cadence/share", "/data/cadence/licenses")
	if err != nil || kb < 500 || kb > 520 {
		t.Fatalf("two trees and a missing one: %d KiB, %v", kb, err)
	}
	if kb, err := sh.UsedKB(ctx, "/data/cadence/licenses"); err != nil || kb != 0 {
		t.Fatalf("a missing tree: %d KiB, %v", kb, err)
	}
}
