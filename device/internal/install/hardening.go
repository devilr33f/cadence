package install

import (
	"context"
	"fmt"
	"path"
)

// HardeningStep is one change hardening makes to a board's own files (rules 1 and 3), such as the vendor's OTA. Check
// only reads. Apply makes the change and first backs up whatever it replaces under backup, its own directory, which
// exists when it runs; Revert puts that backup back. A step whose files nobody has inspected on a device yet checks
// as StepNotInspected and is never applied. A write to the root filesystem keeps its discipline: read-only again on
// every exit path.
type HardeningStep interface {
	Name() string
	Check(ctx context.Context, dev Device) (StepState, error)
	Apply(ctx context.Context, dev Device, backup string) error
	Revert(ctx context.Context, dev Device, backup string) error
}

// StepState is what a step's Check found.
type StepState string

const (
	StepApplied      StepState = "applied"
	StepNotApplied   StepState = "not_applied"
	StepNotInspected StepState = "not_inspected"
)

// Uninspected is a step declared before its files were inspected on a device: it checks as not inspected, and Apply
// and Revert refuse.
type Uninspected string

func (u Uninspected) Name() string { return string(u) }

func (Uninspected) Check(context.Context, Device) (StepState, error) { return StepNotInspected, nil }

func (u Uninspected) Apply(context.Context, Device, string) error { return notInspected(u) }

func (u Uninspected) Revert(context.Context, Device, string) error { return notInspected(u) }

func notInspected(s HardeningStep) error {
	return fmt.Errorf("%w: hardening step %s is not inspected", ErrRefused, s.Name())
}

// stepBackup is where a step keeps what it replaced
func (in *Installer) stepBackup(s HardeningStep) string { return path.Join(in.l.backup, s.Name()) }

// harden applies a step once: an applied one is left as it is and one not inspected is refused. Recovery access is
// checked before and after (rule 2), the backup directory is made before Apply runs, and the step must check as
// applied afterwards
func (in *Installer) harden(ctx context.Context, s HardeningStep) error {
	if done, err := in.stepIs(ctx, s, StepApplied); err != nil || done {
		return err
	}
	if err := in.recovery(ctx); err != nil {
		return err
	}
	if err := in.dev.Mkdir(ctx, in.l.backup, 0o700); err != nil {
		return err
	}
	if err := in.dev.Mkdir(ctx, in.stepBackup(s), 0o700); err != nil {
		return err
	}
	if err := s.Apply(ctx, in.dev, in.stepBackup(s)); err != nil {
		return fmt.Errorf("%s: %w", s.Name(), err)
	}
	return in.stepDone(ctx, s, StepApplied, "did not apply")
}

// unharden reverts an applied step from its backup, under the same checks
func (in *Installer) unharden(ctx context.Context, s HardeningStep) error {
	if done, err := in.stepIs(ctx, s, StepNotApplied); err != nil || done {
		return err
	}
	if err := in.recovery(ctx); err != nil {
		return err
	}
	if err := s.Revert(ctx, in.dev, in.stepBackup(s)); err != nil {
		return fmt.Errorf("%s: %w", s.Name(), err)
	}
	return in.stepDone(ctx, s, StepNotApplied, "did not revert")
}

// stepIs checks a step before a change: true when it is in the state the change would leave, and a refusal when
// nobody inspected it
func (in *Installer) stepIs(ctx context.Context, s HardeningStep, want StepState) (bool, error) {
	state, err := s.Check(ctx, in.dev)
	switch {
	case err != nil:
		return false, err
	case state == StepNotInspected:
		return false, notInspected(s)
	case state == want:
		in.say("%s: %s already", s.Name(), state)
		return true, nil
	case state != StepApplied && state != StepNotApplied:
		return false, fmt.Errorf("%s: unknown state %q", s.Name(), state)
	}
	return false, nil
}

// stepDone checks a step after a change: recovery access kept, and the step in the state the change leaves
func (in *Installer) stepDone(ctx context.Context, s HardeningStep, want StepState, failed string) error {
	if in.o.DryRun {
		return nil
	}
	if err := in.recovery(ctx); err != nil {
		return fmt.Errorf("recovery access lost after %s: %v", s.Name(), err) //nolint:errorlint // a failure, not a refusal
	}
	state, err := s.Check(ctx, in.dev)
	if err != nil {
		return err
	}
	if state != want {
		return fmt.Errorf("%s %s (%s)", s.Name(), failed, state)
	}
	in.say("%s: %s", s.Name(), want)
	return nil
}

// OverlayStub shadows a vendor binary on an overlay root with a stub: Apply writes the stub over Path, Revert writes
// the lower layer's bytes back through the same path. Nothing at Path is ever removed, which would whiteout the lower
// copy (Lower is that copy's path, as the lower layer is mounted).
type OverlayStub struct {
	StepName, Path, Lower string
	Content               []byte
}

func (s *OverlayStub) Name() string { return s.StepName }

func (s *OverlayStub) Check(ctx context.Context, dev Device) (StepState, error) {
	now, ok, err := dev.Sum(ctx, s.Path)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: %s is missing", ErrRefused, s.Path)
	}
	if now == sum(s.Content) {
		return StepApplied, nil
	}
	lower, ok, err := dev.Sum(ctx, s.Lower)
	if err != nil {
		return "", err
	}
	if ok && now == lower {
		return StepNotApplied, nil
	}
	return "", fmt.Errorf("%w: %s differs from both stock and stub", ErrRefused, s.Path)
}

func (s *OverlayStub) Apply(ctx context.Context, dev Device, backup string) error {
	lower, ok, err := dev.Sum(ctx, s.Lower)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s is missing", ErrRefused, s.Lower)
	}
	if err := dev.Write(ctx, path.Join(backup, "lower.sha256"), []byte(lower+"\n"), 0o600); err != nil {
		return err
	}
	return dev.WriteFile(ctx, s.Path, s.Content, "0755")
}

func (s *OverlayStub) Revert(ctx context.Context, dev Device, _ string) error {
	ok, err := dev.Exists(ctx, s.Lower)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s is missing", ErrRefused, s.Lower)
	}
	return dev.CopyFile(ctx, s.Lower, s.Path, "0755")
}

// hardeningState says what each of the board's steps checks as, reading only, and returns those not applied yet; a
// step not inspected is never applied, and while only such steps stand in, the router's OTA block does (M2m-R3)
func (in *Installer) hardeningState(ctx context.Context) ([]HardeningStep, error) {
	var pending []HardeningStep
	applied := 0
	for _, s := range in.p.Hardening {
		state, err := s.Check(ctx, in.dev)
		if err != nil {
			return nil, err
		}
		in.say("hardening %s: %s", s.Name(), state)
		switch state {
		case StepNotApplied:
			pending = append(pending, s)
		case StepApplied:
			applied++
		}
	}
	if len(in.p.Hardening) > 0 && applied+len(pending) == 0 && in.p.RequireOTABlocked {
		in.say("none applied: the router's OTA block stands in for them (--ota-blocked)")
	}
	return pending, nil
}

// applyHardening applies the steps hardeningState found not applied (rule 3), once every refusal of the run is known,
// so a refused run changes nothing; a dry run only says it would
func (in *Installer) applyHardening(ctx context.Context, pending []HardeningStep) error {
	for _, s := range pending {
		if in.o.DryRun {
			in.say("would apply %s", s.Name())
			continue
		}
		if err := in.harden(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
