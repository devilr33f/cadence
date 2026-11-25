package install

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

// a board whose / is an rw overlay (Profile.OverlayRoot) boots cadence through an init script rcS already runs,
// Profile.InitScript, since init reads its table from the lower layer before the overlay exists: install writes the
// launcher over it, and a restore writes the lower copy's bytes back through the merged path. nothing here removes a
// path the lower layer has (a whiteout would hide the stock copy), remounts / or reads the inittab. cadence-run is
// the process the launcher's pid file names: its watchdog feeder, a subshell, has the same command line and outlives
// it through the hook's grace

// stopPolls bound the wait for cadence-run to go after the launcher's stop: it kills cadenced 3 s after TERM, then
// blanks. its hook keeps feeding the watchdog for a grace period meanwhile, so cadence-run is never sent KILL here
const stopPolls = 10

func (in *Installer) byScript() bool { return in.p.InitScript != "" }

// scriptPreflight only reads: the stock copy a restore writes back is there, and an install finds no v1, which a
// board that boots by an init script never runs
func (in *Installer) scriptPreflight(ctx context.Context, install bool) error {
	if ok, err := in.dev.Exists(ctx, in.p.InitScriptLower); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s is missing: the stock's init script could not be given back", ErrRefused, in.p.InitScriptLower)
	}
	if !install {
		return nil
	}
	if ok, err := in.v1Present(ctx); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: a v1 tree is in %s or %s, which %s never runs", ErrRefused, in.l.root, in.l.v1Dir, in.p.Board)
	}
	return nil
}

// supervisor is the cadence-run the launcher's pid file names; ok is false when the file is absent or stale: no such
// pid, or one that is not cadence-run
func (in *Installer) supervisor(ctx context.Context) (Proc, bool, error) {
	raw, ok, err := in.dev.Read(ctx, in.p.InitPidFile)
	if err != nil || !ok {
		return Proc{}, false, err
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if perr != nil {
		return Proc{}, false, nil
	}
	procs, err := in.dev.Processes(ctx)
	if err != nil {
		return Proc{}, false, err
	}
	id := script("cadence-run", in.l.wrapper)
	i := slices.IndexFunc(procs, func(p Proc) bool { return p.Pid == pid && id.match(p) })
	if i < 0 {
		return Proc{}, false, nil
	}
	return procs[i], true, nil
}

// startByScript puts the launcher at InitScript and starts cadence-run through it, stopping a running one through it
// first
func (in *Installer) startByScript(ctx context.Context) error {
	if err := in.writeLauncher(ctx); err != nil {
		return err
	}
	if err := in.stopByScript(ctx); err != nil {
		return err
	}
	if _, err := in.dev.RunScript(ctx, in.p.InitScript, "start"); err != nil {
		return err
	}
	in.say("%s starts %s", in.p.InitScript, in.l.wrapper)
	return nil
}

// writeLauncher writes the launcher at InitScript unless it is there already
func (in *Installer) writeLauncher(ctx context.Context) error {
	if s, ok, err := in.dev.Sum(ctx, in.p.InitScript); err != nil || (ok && s == sum(in.p.InitLauncher)) {
		return err
	}
	if err := in.dev.WriteFile(ctx, in.p.InitScript, in.p.InitLauncher, "0755"); err != nil {
		return err
	}
	in.say("%s is cadence's launcher", in.p.InitScript)
	return nil
}

// stopByScript stops the cadence-run the pid file names through the launcher's stop and waits for it to go, then
// stops whatever of cadence it left as stop does (neither cadenced nor audiod feeds the watchdog). it never runs the
// stock's stop, which would stop the stock assistant and not cadence
func (in *Installer) stopByScript(ctx context.Context) error {
	sup, running, err := in.supervisor(ctx)
	if err != nil {
		return err
	}
	if running {
		stock, err := in.scriptIsStock(ctx)
		if err != nil {
			return err
		}
		if stock {
			return fmt.Errorf("cadence-run (pid %d) runs, but %s is the stock's own: its stop would stop the stock, not cadence",
				sup.Pid, in.p.InitScript)
		}
		if _, err := in.dev.RunScript(ctx, in.p.InitScript, "stop"); err != nil {
			return err
		}
		if in.o.DryRun {
			return nil
		}
		for range stopPolls {
			in.o.sleep(pollEvery)
			if _, running, err = in.supervisor(ctx); err != nil || !running {
				break
			}
		}
		if err != nil {
			return err
		}
		if running {
			return fmt.Errorf("cadence-run (pid %d) still runs %s after %s stop: nothing is started beside it", sup.Pid,
				stopPolls*pollEvery, in.p.InitScript)
		}
		in.say("cadence-run stopped")
	}
	return in.stop(ctx, []identity{binary("cadenced", in.l.daemon), binary("cadence-audiod", in.l.audiod)}, nil)
}

// scriptIsStock is InitScript holding the lower copy's bytes
func (in *Installer) scriptIsStock(ctx context.Context) (bool, error) {
	lower, ok, err := in.dev.Sum(ctx, in.p.InitScriptLower)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("%s is missing", in.p.InitScriptLower)
	}
	cur, ok, err := in.dev.Sum(ctx, in.p.InitScript)
	return ok && cur == lower, err
}

// verifyLauncher is verify's part on a board that boots by an init script: the pid file names a live cadence-run, and
// the script is the launcher, so the next boot starts cadence too
func (in *Installer) verifyLauncher(ctx context.Context) error {
	if !in.byScript() {
		return nil
	}
	running := false
	for i := range startPolls {
		if i > 0 {
			in.o.sleep(logEvery)
		}
		var err error
		if _, running, err = in.supervisor(ctx); err != nil {
			return err
		} else if running {
			break
		}
	}
	if !running {
		return fmt.Errorf("cadence-run does not run: %s names no live one", in.p.InitPidFile)
	}
	if s, ok, err := in.dev.Sum(ctx, in.p.InitScript); err != nil {
		return err
	} else if !ok || s != sum(in.p.InitLauncher) {
		return fmt.Errorf("%s is not cadence's launcher: the next boot would not start cadence", in.p.InitScript)
	}
	return nil
}

// startStockByScript gives InitScript the lower copy's bytes back and, unless the stock runs already, hands the
// watchdog over: the hook's release of its feeder and the stock's start in one command, so the stock opens the
// watchdog within its timeout. every read comes first, so only the lower copy's write sits between them and the
// hand-over; with the stock running nothing is released, as the hook's pid file may be stale by then
func (in *Installer) startStockByScript(ctx context.Context) error {
	lower, ok, err := in.dev.Read(ctx, in.p.InitScriptLower)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s is missing", in.p.InitScriptLower)
	}
	cur, ok, err := in.dev.Sum(ctx, in.p.InitScript)
	if err != nil {
		return err
	}
	wrapper, err := in.dev.Exists(ctx, in.l.wrapper)
	if err != nil {
		return err
	}
	running, err := in.launcherRuns(ctx)
	if err != nil {
		return err
	}
	if !ok || cur != sum(lower) {
		if err := in.dev.WriteFile(ctx, in.p.InitScript, lower, "0755"); err != nil {
			return err
		}
		in.say("%s is the stock's again", in.p.InitScript)
	}
	if running {
		in.say("%s runs already", in.p.Stock.Launcher)
		return nil
	}
	if !wrapper {
		// no wrapper, so no hook ran a feeder to release: the stock starts alone
		if _, err := in.dev.RunScript(ctx, in.p.InitScript, "start"); err != nil {
			return err
		}
		in.say("started %s", in.p.InitScript)
		return nil
	}
	if _, err := in.dev.HandOver(ctx, in.l.wrapper, in.p.InitScript); err != nil {
		return err
	}
	in.say("the watchdog handed over, started %s", in.p.InitScript)
	return nil
}

// startPlatformScripts runs each platform script with start while its process does not run, as the scripts start a
// second copy; one that fails is said and left, as startPlatform does
func (in *Installer) startPlatformScripts(ctx context.Context) {
	for _, s := range in.p.PlatformScripts {
		if ok, err := in.dev.Running(ctx, s.Process); err != nil {
			in.say("%s: %v", s.Script, err)
			continue
		} else if ok {
			in.say("%s runs already", s.Process)
			continue
		}
		if _, err := in.dev.RunScript(ctx, s.Script, "start"); err != nil {
			in.say("%s did not start: %v", s.Script, err)
			continue
		}
		in.say("started %s", s.Script)
	}
}

// v1Present is v1's supervisor in the data root or moved aside
func (in *Installer) v1Present(ctx context.Context) (bool, error) {
	for _, p := range []string{path.Join(in.l.v1Dir, "cadenced.init"), in.l.v1Init} {
		if ok, err := in.dev.Exists(ctx, p); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}
