package install

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	stubPath  = "/usr/bin/ota-updater"
	stubLower = "/rom/usr/bin/ota-updater"
)

var (
	// past Read's cap, as the vendor's fwupdater is
	stubStock   = bytes.Repeat([]byte("\x7fELF stock ota updater\n"), 14000)
	stubContent = []byte("#!/bin/sh\nexit 0\n")
)

func testStub() *OverlayStub {
	return &OverlayStub{StepName: "ota-stub", Path: stubPath, Lower: stubLower, Content: stubContent}
}

func stubSpeaker(t *testing.T) *fake {
	t.Helper()
	f := overlaySpeaker(t)
	f.put(stubLower, stubStock, 0o755)
	f.put(stubPath, stubStock, 0o755)
	f.p.Hardening = []HardeningStep{testStub()}
	return f
}

func TestOverlayStubCheckStates(t *testing.T) {
	ctx := context.Background()
	f := stubSpeaker(t)
	s := testStub()
	if st, err := s.Check(ctx, f); err != nil || st != StepNotApplied {
		t.Fatalf("stock: %q, %v", st, err)
	}
	f.put(stubPath, stubContent, 0o755)
	if st, err := s.Check(ctx, f); err != nil || st != StepApplied {
		t.Fatalf("stub: %q, %v", st, err)
	}
	f.put(stubPath, []byte("something else"), 0o755)
	if _, err := s.Check(ctx, f); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), stubPath+" differs from both stock and stub") {
		t.Fatalf("other: %v", err)
	}
}

func TestInstallAppliesInspectedHardening(t *testing.T) {
	f := stubSpeaker(t)
	out := mustInstallOverlay(t, f)
	if string(f.get(stubPath)) != string(stubContent) || !strings.Contains(out, "ota-stub: applied") {
		t.Fatalf("not applied:\n%s", out)
	}
	if string(f.get("/data/cadence/backup/ota-stub/lower.sha256")) != sum(stubStock)+"\n" {
		t.Fatalf("backup: %q", f.get("/data/cadence/backup/ota-stub/lower.sha256"))
	}
	n := 0
	for _, op := range f.changes() {
		if strings.HasPrefix(op, "write-file "+stubPath+" ") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d writes of the stub", n)
	}
	f.ops = nil
	if _, err := install(t, f, overlayOpts(t, f)); err != nil {
		t.Fatal(err)
	}
	for _, op := range f.changes() {
		if strings.Contains(op, stubPath) {
			t.Fatalf("a second install touched the stub: %q", op)
		}
	}
}

// a run that refuses changes nothing, the stubs included: they are applied only once every refusal is known
func TestRefusedInstallAppliesNoHardening(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*fake, *Options)
		want  string
	}{
		"no code":     {func(_ *fake, o *Options) { o.Code = "" }, "--code (from the dashboard) is required"},
		"http server": {func(_ *fake, o *Options) { o.Server = "http://cadence.example" }, "--server must be"},
		"space":       {func(f *fake, _ *Options) { f.free["/data"] = 1000 }, "/data has 1000 KiB free"},
	} {
		t.Run(name, func(t *testing.T) {
			f := stubSpeaker(t)
			o := overlayOpts(t, f)
			tc.setup(f, &o)
			out, err := install(t, f, o)
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.want, out)
			}
			if string(f.get(stubPath)) != string(stubStock) || len(f.changes()) != 0 {
				t.Fatalf("a refused run changed: %q\n%s", f.changes(), out)
			}
			if !strings.Contains(out, "hardening ota-stub: not_applied") {
				t.Fatalf("the step's state was not reported:\n%s", out)
			}
		})
	}
}

// a dry run reports the step it would apply, and applies none
func TestDryRunWouldApplyHardening(t *testing.T) {
	f := stubSpeaker(t)
	o := overlayOpts(t, f)
	o.DryRun = true
	out, err := install(t, f, o)
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would apply ota-stub") || string(f.get(stubPath)) != string(stubStock) {
		t.Fatalf("stub %q:\n%s", f.get(stubPath), out)
	}
}

func TestOverlayStubRevertWritesLowerBytes(t *testing.T) {
	ctx := context.Background()
	f := stubSpeaker(t)
	f.put(stubPath, stubContent, 0o755)
	f.ops = nil
	if err := testStub().Revert(ctx, f, "/data/cadence/backup/ota-stub"); err != nil {
		t.Fatal(err)
	}
	if string(f.get(stubPath)) != string(stubStock) {
		t.Fatal("stock bytes not written back")
	}
	ops := f.changes()
	if len(ops) != 1 || ops[0] != "copy-file "+stubLower+" "+stubPath+" 0755" {
		t.Fatalf("ops %q", ops)
	}
}
