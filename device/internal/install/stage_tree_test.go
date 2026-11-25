package install

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestCheckFailureRemovesTheTreeAndResumeFitsAgain(t *testing.T) {
	f := v1Speaker(t)
	f.fail["check "] = errors.New("manifest differs")
	_, err := install(t, f, runOpts(t, f))
	if err == nil || !strings.Contains(err.Error(), "manifest differs") {
		t.Fatalf("err = %v", err)
	}
	if len(f.under("/data/cadence/.install/tree")) != 0 {
		t.Fatal("the tree was left behind")
	}
	delete(f.fail, "check ")
	out, err := install(t, f, runOpts(t, f))
	if err != nil {
		t.Fatalf("resume: %v\n%s", err, out)
	}
}

func TestLeftoverTreeIsRemovedBeforeUnpacking(t *testing.T) {
	f := v1Speaker(t)
	f.put("/data/cadence/.install/tree/stale", []byte("x"), 0o644)
	mustInstall(t, f)
	ops := f.changes()
	rm := slices.Index(ops, "rm /data/cadence/.install/tree")
	un := slices.Index(ops, "unpack /data/cadence/.install/cadence.tar /data/cadence/.install/tree")
	if rm < 0 || un < 0 || rm > un {
		t.Fatalf("commands: %q", ops)
	}
}

func TestPlaceFailureKeepsTheTree(t *testing.T) {
	f := v1Speaker(t)
	f.fail["mv /data/cadence/.install/tree/"] = errors.New("cut short")
	_, err := install(t, f, runOpts(t, f))
	if err == nil {
		t.Fatal("no error")
	}
	if !f.has("/data/cadence/.install/tree/MANIFEST") {
		t.Fatal("the tree was removed after placing started")
	}
}
