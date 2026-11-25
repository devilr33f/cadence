package install

import (
	"bytes"
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"testing"
)

// spyStep is a step that only counts what it was asked to do
type spyStep struct {
	name              string
	state             StepState
	applied, reverted int
}

func (s *spyStep) Name() string                                     { return s.name }
func (s *spyStep) Check(context.Context, Device) (StepState, error) { return s.state, nil }
func (s *spyStep) Apply(context.Context, Device, string) error {
	s.applied++
	return nil
}
func (s *spyStep) Revert(context.Context, Device, string) error {
	s.reverted++
	return nil
}

// fileStep replaces one file under the data root, as a real step would replace one of the vendor's: its Apply backs
// the file up into its backup dir first, its Revert copies the backup back. lazy makes an Apply that changes nothing
type fileStep struct {
	file, content string
	lazy          bool
	backups       []string
}

func (s *fileStep) Name() string { return "test-step" }

func (s *fileStep) Check(ctx context.Context, dev Device) (StepState, error) {
	raw, _, err := dev.Read(ctx, s.file)
	if err != nil {
		return "", err
	}
	if string(raw) == s.content {
		return StepApplied, nil
	}
	return StepNotApplied, nil
}

func (s *fileStep) Apply(ctx context.Context, dev Device, backup string) error {
	s.backups = append(s.backups, backup)
	if ok, err := dev.Exists(ctx, backup); err != nil || !ok {
		return errors.Join(errors.New("no backup dir"), err)
	}
	if err := dev.Copy(ctx, s.file, path.Join(backup, path.Base(s.file)), 0o600); err != nil || s.lazy {
		return err
	}
	return dev.Write(ctx, s.file, []byte(s.content), 0o644)
}

func (s *fileStep) Revert(ctx context.Context, dev Device, backup string) error {
	s.backups = append(s.backups, backup)
	return dev.Copy(ctx, path.Join(backup, path.Base(s.file)), s.file, 0o644)
}

// hardenWith runs harden or unharden on a pickle-shaped device, counting the recovery checks
func hardenWith(t *testing.T, f *fake, run func(*Installer) error) (out string, probes int, err error) {
	t.Helper()
	var buf bytes.Buffer
	o := runOpts(t, f)
	o.Out = &buf
	o.Recovery = []func(context.Context) error{func(context.Context) error {
		probes++
		return nil
	}}
	in := New(f, f.p, o)
	in.phase = "harden"
	err = run(in)
	return buf.String(), probes, err
}

// the frame's contract: a step's backup dir exists before its Apply, recovery access is checked before and after
// (rule 2), it must check as applied afterwards, a second run changes nothing, and Revert puts the backup back
func TestHardeningFrame(t *testing.T) {
	f := v1Speaker(t)
	f.put("/data/cadence/vendor.conf", []byte("ota=on\n"), 0o644)
	step := &fileStep{file: "/data/cadence/vendor.conf", content: "ota=off\n"}
	ctx := context.Background()

	_, probes, err := hardenWith(t, f, func(in *Installer) error { return in.harden(ctx, step) })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"mkdir /data/cadence/backup 700", "mkdir /data/cadence/backup/test-step 700",
		"cp /data/cadence/vendor.conf /data/cadence/backup/test-step/vendor.conf 600", "write /data/cadence/vendor.conf 644",
	}
	if got := f.changes(); !slices.Equal(got, want) || probes != 2 {
		t.Fatalf("commands %q, %d recovery checks", got, probes)
	}
	if string(f.get("/data/cadence/vendor.conf")) != "ota=off\n" || string(f.get("/data/cadence/backup/test-step/vendor.conf")) != "ota=on\n" {
		t.Fatal("not applied, or no backup")
	}

	f.ops = nil
	if out, _, err := hardenWith(t, f, func(in *Installer) error { return in.harden(ctx, step) }); err != nil ||
		len(f.changes()) != 0 || len(step.backups) != 1 || !strings.Contains(out, "test-step: applied already") {
		t.Fatalf("a second run: %v, %q\n%s", err, f.changes(), out)
	}

	f.ops = nil
	_, probes, err = hardenWith(t, f, func(in *Installer) error { return in.unharden(ctx, step) })
	if err != nil || probes != 2 || step.backups[1] != "/data/cadence/backup/test-step" {
		t.Fatalf("revert: %v, %d recovery checks, backups %q", err, probes, step.backups)
	}
	if string(f.get("/data/cadence/vendor.conf")) != "ota=on\n" {
		t.Fatal("not reverted byte for byte")
	}
	f.ops = nil
	if _, _, err := hardenWith(t, f, func(in *Installer) error { return in.unharden(ctx, step) }); err != nil || len(f.changes()) != 0 {
		t.Fatalf("a second revert: %v, %q", err, f.changes())
	}
}

func TestHardeningFrameRefuses(t *testing.T) {
	ctx := context.Background()
	// an Apply that leaves the step unapplied fails
	f := v1Speaker(t)
	f.put("/data/cadence/vendor.conf", []byte("ota=on\n"), 0o644)
	if _, _, err := hardenWith(t, f, func(in *Installer) error {
		return in.harden(ctx, &fileStep{file: "/data/cadence/vendor.conf", content: "ota=off\n", lazy: true})
	}); err == nil || !strings.Contains(err.Error(), "test-step did not apply") {
		t.Fatalf("err = %v", err)
	}

	// a step nobody inspected is neither applied nor reverted
	spy := &spyStep{name: "ota-key", state: StepNotInspected}
	for name, run := range map[string]func(*Installer) error{
		"harden":   func(in *Installer) error { return in.harden(ctx, spy) },
		"unharden": func(in *Installer) error { return in.unharden(ctx, spy) },
	} {
		f := v1Speaker(t)
		if _, _, err := hardenWith(t, f, run); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "ota-key is not inspected") ||
			len(f.changes()) != 0 {
			t.Errorf("%s: err = %v, changes %q", name, err, f.changes())
		}
	}
	if spy.applied+spy.reverted != 0 {
		t.Fatalf("applied %d, reverted %d", spy.applied, spy.reverted)
	}

	// access lost by a step stops it as a failure
	f = v1Speaker(t)
	f.put("/data/cadence/vendor.conf", []byte("ota=on\n"), 0o644)
	var buf bytes.Buffer
	o := runOpts(t, f)
	o.Out = &buf
	calls := 0
	o.Recovery = []func(context.Context) error{func(context.Context) error {
		if calls++; calls > 1 {
			return errors.New("ssh root@192.0.2.1: Connection refused")
		}
		return nil
	}}
	err := New(f, f.p, o).harden(ctx, &fileStep{file: "/data/cadence/vendor.conf", content: "ota=off\n"})
	if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "recovery access lost after test-step") {
		t.Fatalf("err = %v", err)
	}
}

// a step declared before its files were inspected checks as not inspected and refuses to change anything
func TestUninspectedStep(t *testing.T) {
	s := Uninspected("ota-script-stub")
	ctx := context.Background()
	if state, err := s.Check(ctx, nil); s.Name() != "ota-script-stub" || state != StepNotInspected || err != nil {
		t.Fatalf("%s: %s %v", s.Name(), state, err)
	}
	if !errors.Is(s.Apply(ctx, nil, "/data/cadence/backup/ota-script-stub"), ErrRefused) || !errors.Is(s.Revert(ctx, nil, ""), ErrRefused) {
		t.Fatal("an uninspected step changed something")
	}
}
