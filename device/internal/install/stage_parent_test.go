package install

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/install/busyboxtest"
)

// the stage dir is made by stage, so preflight (and a dry run, which writes nothing) checks its nearest parent
func missingStage(t *testing.T, parentKB int64) *fake {
	f := stagedSpeaker(t)
	f.mkdirAll("/tmp")
	delete(f.free, "/tmp/cadence-install")
	f.free["/tmp"] = parentKB
	return f
}

func TestMissingStageDirIsCheckedOnItsParent(t *testing.T) {
	f := missingStage(t, 1<<20)
	mustInstall(t, f)
}

func TestMissingStageDirRefusesWhenItsParentIsFull(t *testing.T) {
	f := missingStage(t, 1)
	_, err := install(t, f, runOpts(t, f))
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "/tmp has 1 KiB free") {
		t.Fatalf("err = %v", err)
	}
	if len(f.changes()) != 0 {
		t.Fatalf("a refused run changed: %q", f.changes())
	}
}

func TestFreeKBOfAMissingPathIsAnError(t *testing.T) {
	c := busyboxtest.Start(t)
	c.MustSh("mkdir -p /fake/tmp")
	sh := NewShell(box{c}, ShellOptions{Root: "/fake"})
	ctx := context.Background()
	if kb, err := sh.FreeKB(ctx, "/tmp"); err != nil || kb <= 0 {
		t.Fatalf("an existing path: %d, %v", kb, err)
	}
	if _, err := sh.FreeKB(ctx, "/tmp/nope"); err == nil || !strings.Contains(err.Error(), "status 2") {
		t.Fatalf("a missing path: err = %v", err)
	}
}
