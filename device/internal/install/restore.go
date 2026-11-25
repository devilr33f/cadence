package install

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
)

// RestoreStock gives a board that keeps its stock assistant back to it (M2m-D2): cadence's line out of init, every
// other byte as it was, v2 stopped and the lights blanked, then the vendor's start script run detached, never the
// boot routine its init script runs. A board that boots by an init script has cadence-run stopped through the
// launcher instead, the script's stock bytes written back, the hook's watchdog feeder released and the stock's start
// run right after. v2's tree and config stay, and install.state forgets start-v2, so a later install starts v2 again.
// The stock brings its updater back, so a board without an OTA block of its own needs the router to block the
// vendor's OTA (--ota-blocked). Every step checks the device first, so a run cut short can be repeated.
func (in *Installer) RestoreStock(ctx context.Context) error {
	in.phase = "preflight"
	if in.p.Stock == nil {
		return in.failed(fmt.Errorf("%w: %s keeps no stock assistant to restore", ErrRefused, in.p.Board))
	}
	if err := in.otaBlocked(in.p.RequireOTABlocked, "the stock assistant brings its updater back"); err != nil {
		return in.failed(err)
	}
	for _, check := range []func(context.Context) error{
		func(ctx context.Context) error { return in.preflight(ctx, false) }, in.noV1, in.noStrangers, in.loadState,
	} {
		if err := check(ctx); err != nil {
			return in.failed(err)
		}
	}
	in.say("ok")
	if err := in.dev.Mkdir(ctx, in.l.work, 0o700); err != nil {
		return in.failed(err)
	}
	in.phase = "stop-v2"
	if err := in.stopV2(ctx, "restore-stock"); err != nil {
		return in.failed(err)
	}
	in.phase = "start-stock"
	if err := in.startStock(ctx); err != nil {
		return in.failed(err)
	}
	in.startPlatform(ctx)
	in.phase = "verify"
	if in.o.DryRun {
		in.say("would check recovery access and the stock assistant running")
		return nil
	}
	if err := in.verifyStock(ctx); err != nil {
		return in.failed(err)
	}
	if err := in.dev.Remove(ctx, in.l.work); err != nil {
		return in.failed(err)
	}
	in.say("the stock assistant runs; an install with the tarball starts cadence v2 again")
	return nil
}

// noV1 refuses a device whose init runs v1: v1 would go on suspending the stock, and its own rollback.sh restores it.
// an overlay root's inittab is never read, and an install there refuses a v1 tree
func (in *Installer) noV1(ctx context.Context) error {
	if in.p.OverlayRoot {
		return nil
	}
	tab, _, err := in.dev.Read(ctx, in.p.Inittab)
	if err != nil {
		return err
	}
	if hasEntry(tab, in.l.v1Entry) {
		return fmt.Errorf("%w: v1 runs from %s: v1's own rollback.sh --restore-stock --ota-blocked restores the stock", ErrRefused,
			in.p.Inittab)
	}
	return nil
}

// noStrangers refuses a process run from v2's bin/ that cadence-install does not know by its command line: stopping
// signals only the processes it knows, and the stock must not start beside one left running
func (in *Installer) noStrangers(ctx context.Context) error {
	from, err := in.dev.RunningFrom(ctx, path.Dir(in.l.daemon))
	if err != nil || len(from) == 0 {
		return err
	}
	procs, err := in.dev.Processes(ctx)
	if err != nil {
		return err
	}
	known := matching(in.v2Processes(), procs)
	for _, p := range from {
		if !slices.ContainsFunc(known, func(k Proc) bool { return k.Pid == p.Pid }) {
			return fmt.Errorf("%w: pid %d runs %s, but not as cadence runs it: it would not be stopped, and the stock "+
				"must not start beside it", ErrRefused, p.Pid, p.Exe)
		}
	}
	return nil
}

// startStock runs the vendor's start script once nothing runs from v2's bin/, unless its launcher runs already
func (in *Installer) startStock(ctx context.Context) error {
	s := in.p.Stock
	if !in.o.DryRun {
		left, err := in.dev.RunningFrom(ctx, path.Dir(in.l.daemon))
		if err != nil {
			return err
		}
		if len(left) > 0 {
			return fmt.Errorf("pid %d still runs from %s: the stock is not started beside it", left[0].Pid, path.Dir(in.l.daemon))
		}
	}
	// init no longer runs v2, so a later install must put it back
	if err := in.unmark(ctx, "start-v2", "verify"); err != nil {
		return err
	}
	if in.byScript() {
		return in.startStockByScript(ctx)
	}
	if running, err := in.launcherRuns(ctx); err != nil {
		return err
	} else if running {
		in.say("%s runs already", s.Launcher)
		return nil
	}
	if err := in.dev.StartScript(ctx, s.Start); err != nil {
		return err
	}
	in.say("started %s", s.Start)
	return nil
}

// startPlatform starts the platform daemons and scripts the board's hook stopped while cadence ran; one that does not
// start is said and left, as the device stays reachable and its stock or v1 runs all the same
func (in *Installer) startPlatform(ctx context.Context) {
	for _, d := range in.p.PlatformDaemons {
		if err := in.dev.StartDaemon(ctx, d.Exe, d.Args); err != nil {
			in.say("%s did not start: %v", d.Exe, err)
			continue
		}
		in.say("started %s", d.Exe)
	}
	in.startPlatformScripts(ctx)
}

// launcherRuns looks where the launcher lies, which may be outside the stock's directory (an overlay board's
// /usr/bin/voice-agent)
func (in *Installer) launcherRuns(ctx context.Context) (bool, error) {
	procs, err := in.dev.RunningFrom(ctx, path.Dir(in.p.Stock.Launcher))
	return slices.ContainsFunc(procs, in.isLauncher), err
}

// verifyStock waits for the stock's launcher: recovery access kept, v2 gone
func (in *Installer) verifyStock(ctx context.Context) error {
	if err := in.recovery(ctx); err != nil {
		return fmt.Errorf("lost after restoring the stock: %v", err) //nolint:errorlint // a failure, not a refusal
	}
	procs, err := in.dev.Processes(ctx)
	if err != nil {
		return err
	}
	if left := matching(in.v2Processes(), procs); len(left) > 0 {
		return fmt.Errorf("%s still runs (pid %d)", matchOf(in.v2Processes(), left[0]), left[0].Pid)
	}
	for i := range startPolls {
		if i > 0 {
			in.o.sleep(logEvery)
		}
		if running, err := in.launcherRuns(ctx); err != nil || running {
			if err == nil {
				in.say("recovery access kept, the stock assistant runs")
			}
			return err
		}
	}
	return fmt.Errorf("%s does not run", in.p.Stock.Launcher)
}

// unmark drops steps from install.state, which is written only when it held one
func (in *Installer) unmark(ctx context.Context, steps ...string) error {
	left := slices.DeleteFunc(slices.Clone(in.steps), func(s string) bool { return slices.Contains(steps, s) })
	if len(left) == len(in.steps) {
		return nil
	}
	if !in.o.DryRun {
		in.steps = left
	}
	return in.dev.Write(ctx, in.l.state, []byte(strings.Join(left, "\n")+"\n"), 0o600)
}
