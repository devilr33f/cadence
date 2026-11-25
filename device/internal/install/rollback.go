package install

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
)

// ToV1 swaps back: v2 out of init and stopped, its tree moved to v2/ and v1's back in place, the owner's current
// level handed to v1, and v1's inittab line restored, byte for byte from the backup when nothing else changed.
// Every step checks the device first, so a run cut short can be repeated.
func (in *Installer) ToV1(ctx context.Context) error {
	in.phase = "preflight"
	if in.byScript() {
		return in.failed(fmt.Errorf("%w: %s boots cadence through %s and never ran v1: --restore-stock gives it back to its "+
			"stock", ErrRefused, in.p.Board, in.p.InitScript))
	}
	if err := in.preflight(ctx, false); err != nil {
		return in.failed(err)
	}
	if err := in.hasV1(ctx); err != nil {
		return in.failed(err)
	}
	if err := in.loadState(ctx); err != nil {
		return in.failed(err)
	}
	in.say("ok")
	if err := in.dev.Mkdir(ctx, in.l.work, 0o700); err != nil {
		return in.failed(err)
	}
	stopV2 := func(ctx context.Context) error { return in.stopV2(ctx, "to-v1") }
	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{{"stop-v2", stopV2}, {"restore-v1", in.restoreV1}, {"start-platform", func(ctx context.Context) error {
		in.startPlatform(ctx)
		return nil
	}}, {"start-v1", in.startV1}} {
		in.phase = step.name
		if err := step.run(ctx); err != nil {
			return in.failed(err)
		}
	}
	in.phase = "verify"
	if in.o.DryRun {
		in.say("would check recovery access and v1 running")
		return nil
	}
	if err := in.verifyV1(ctx); err != nil {
		return in.failed(err)
	}
	in.say("cadence v1 runs")
	return nil
}

// hasV1 refuses a device with no v1 to go back to, moved aside or in place
func (in *Installer) hasV1(ctx context.Context) error {
	if ok, err := in.v1Present(ctx); err != nil || ok {
		return err
	}
	return fmt.Errorf("%w: no v1 in %s or %s", ErrRefused, in.l.v1Dir, in.l.root)
}

// stopV2 takes v2 out of init, its inittab backup tagged tag, stops it and blanks the lights; a board that boots by
// an init script stops it through the launcher instead, its inittab untouched
func (in *Installer) stopV2(ctx context.Context, tag string) error {
	if in.byScript() {
		if err := in.stopByScript(ctx); err != nil {
			return err
		}
	} else {
		if err := in.setInittab(ctx, tag, in.l.v2Entry, func(cur []byte) ([]byte, error) {
			return withoutEntry(cur, in.l.v2Entry), nil
		}); err != nil {
			return err
		}
		if err := in.stop(ctx, in.v2Processes(), nil); err != nil {
			return err
		}
	}
	if ok, err := in.dev.Exists(ctx, in.l.wrapper); err != nil {
		return err
	} else if ok {
		in.blank(ctx, in.l.wrapper)
	}
	return nil
}

// restoreV1 moves v2's tree to v2/ (an older one there is replaced) and v1's back, then hands v1 the level v2 kept
func (in *Installer) restoreV1(ctx context.Context) error {
	if ok, err := in.dev.Exists(ctx, in.l.v1Dir); err != nil || !ok {
		return err
	}
	if !in.done("v2-aside") {
		if err := in.dev.Remove(ctx, in.l.v2Dir); err != nil {
			return err
		}
		n, err := in.moveEntries(ctx, in.l.root, in.l.v2Dir, kept)
		if err != nil {
			return err
		}
		if err := in.mark(ctx, "v2-aside"); err != nil {
			return err
		}
		in.say("moved %d entries of v2 to %s", n, in.l.v2Dir)
	}
	n, err := in.moveEntries(ctx, in.l.v1Dir, in.l.root, nil)
	if err != nil {
		return err
	}
	in.say("moved %d entries of v1 back to %s", n, in.l.root)
	// before v1/ goes: a run cut short after it is past this
	if err := in.levelToV1(ctx); err != nil {
		return err
	}
	if left, err := in.dev.List(ctx, in.l.v1Dir); err != nil || (len(left) > 0 && !in.o.DryRun) {
		return err
	}
	return in.dev.Remove(ctx, in.l.v1Dir)
}

// levelToV1 writes v2's level in v1's format, so v1 starts at the level the owner set last (rule 7); without one, v1
// keeps its own
func (in *Installer) levelToV1(ctx context.Context) error {
	raw, ok, err := in.dev.Read(ctx, path.Join(in.l.v2Dir, "state", "volume"))
	if err != nil {
		return err
	}
	if !ok {
		in.say("v2 kept no level: v1 keeps its own")
		return nil
	}
	data, level, ok := toV1(raw)
	if !ok {
		in.say("v2 kept no level: v1 keeps its own")
		return nil
	}
	if err := in.dev.Mkdir(ctx, path.Dir(in.l.volume), 0o700); err != nil {
		return err
	}
	if err := in.dev.Write(ctx, in.l.volume, data, 0o600); err != nil {
		return err
	}
	in.say("volume %d", level)
	return nil
}

// startV1 gives init v1's line back: the backup taken before it was removed, when the inittab is that backup
// without it, else v1's line right after the anchor as v1's install.sh placed it
func (in *Installer) startV1(ctx context.Context) error {
	backup, err := in.v1Backup(ctx)
	if err != nil {
		return err
	}
	if err := in.setInittab(ctx, "restore-v1", in.l.v1Entry, func(cur []byte) ([]byte, error) {
		switch {
		case hasEntry(cur, in.l.v1Entry):
			return cur, nil
		case backup != nil && bytes.Equal(withoutEntry(backup, in.l.v1Entry), cur):
			return backup, nil
		}
		return withEntry(cur, in.l.v1Entry, in.p.Anchor)
	}); err != nil {
		return err
	}
	in.say("init runs %s", in.l.v1Init)
	if err := in.dev.Remove(ctx, in.l.state); err != nil {
		return err
	}
	return in.dev.Remove(ctx, in.l.work)
}

// v1Backup is the newest inittab backup that holds v1's line, nil when there is none
func (in *Installer) v1Backup(ctx context.Context) ([]byte, error) {
	names, err := in.dev.List(ctx, in.l.backup)
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	for _, n := range slices.Backward(names) {
		if !strings.HasPrefix(n, "inittab.") {
			continue
		}
		raw, ok, err := in.dev.Read(ctx, path.Join(in.l.backup, n))
		if err != nil {
			return nil, err
		}
		if ok && hasEntry(raw, in.l.v1Entry) {
			return raw, nil
		}
	}
	return nil, nil
}

func (in *Installer) verifyV1(ctx context.Context) error {
	if err := in.recovery(ctx); err != nil {
		return fmt.Errorf("lost after the swap: %v", err) //nolint:errorlint // drops ErrRefused on purpose
	}
	if err := in.waitRunning(ctx, script("v1's supervisor", in.l.v1Init)); err != nil {
		return err
	}
	procs, err := in.dev.Processes(ctx)
	if err != nil {
		return err
	}
	if left := matching(in.v2Processes(), procs); len(left) > 0 {
		return fmt.Errorf("%s still runs (pid %d)", matchOf(in.v2Processes(), left[0]), left[0].Pid)
	}
	in.say("recovery access kept, v1's supervisor runs")
	return nil
}
