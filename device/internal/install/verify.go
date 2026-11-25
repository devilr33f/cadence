package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// preflight only reads: the device must be the board, keep its recovery access and keep its vendor assistant off as
// the board does; a swap to v2 also needs the tested firmware (or the owner's acceptance). the room for the tree is
// checked once install.state is read, while stage is still to run
func (in *Installer) preflight(ctx context.Context, install bool) error {
	if err := in.p.bootErr(); err != nil {
		return fmt.Errorf("%w: %s's profile: %w", ErrRefused, in.p.Board, err)
	}
	model, ok, err := in.dev.Read(ctx, in.p.ModelPath)
	if err != nil {
		return err
	}
	m := string(bytes.ReplaceAll(model, []byte{0}, nil))
	if !ok || !strings.HasPrefix(m, in.p.ModelPrefix) || (in.p.Model != "" && m != in.p.Model) {
		return fmt.Errorf("%w: not a %s board", ErrRefused, in.p.Board)
	}
	kernel, _, err := in.dev.Read(ctx, in.p.KernelPath)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(string(kernel)), in.p.KernelFamily) {
		return fmt.Errorf("%w: kernel %q is not %sx", ErrRefused, strings.TrimSpace(string(kernel)), in.p.KernelFamily)
	}
	if err := in.variant(ctx); err != nil {
		return err
	}
	missing, err := in.dev.Missing(ctx, in.p.Tools)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: the device lacks %s", ErrRefused, strings.Join(missing, ", "))
	}
	if err := in.recovery(ctx); err != nil {
		return err
	}
	if err := in.vendor(ctx); err != nil {
		return err
	}
	if in.byScript() {
		if err := in.scriptPreflight(ctx, install); err != nil {
			return err
		}
	}
	if !install {
		return nil
	}
	for _, r := range in.p.RefuseIfPresent {
		if ok, err := in.dev.Exists(ctx, r.Path); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%w: %s exists: %s", ErrRefused, r.Path, r.Reason)
		}
	}
	return in.firmware(ctx)
}

// recovery is rule 2: the transports answer, init keeps running adbd and dropbear (an overlay root's inittab is never
// read: its recovery daemons are not init's), and the recovery daemons run
func (in *Installer) recovery(ctx context.Context) error {
	for _, probe := range in.o.Recovery {
		if err := probe(ctx); err != nil {
			return fmt.Errorf("%w: recovery access: %w", ErrRefused, err)
		}
	}
	if !in.p.OverlayRoot {
		tab, _, err := in.dev.Read(ctx, in.p.Inittab)
		if err != nil {
			return err
		}
		for _, e := range in.p.Recovery {
			if !hasEntry(tab, e) {
				return fmt.Errorf("%w: recovery access: %s has no %s entry", ErrRefused, in.p.Inittab, e)
			}
		}
	}
	for _, d := range in.p.RecoveryDaemons {
		if ok, err := in.dev.Running(ctx, d); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w: recovery access: %s is not running", ErrRefused, d)
		}
	}
	return nil
}

// vendor is rule 4: the vendor assistant and its updater were removed and run no more, or, on a board that keeps
// them (M2m-R2), they are installed for the wrapper's hook to suspend; either way init's table starts nothing of
// theirs
func (in *Installer) vendor(ctx context.Context) error {
	if err := in.stockPresent(ctx); err != nil {
		return err
	}
	for _, p := range in.p.Vendor {
		if ok, err := in.dev.Exists(ctx, p); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%w: %s is present: the vendor assistant or its updater is still installed", ErrRefused, p)
		}
	}
	for _, d := range in.p.VendorDaemons {
		if ok, err := in.dev.Running(ctx, d); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%w: %s is running", ErrRefused, d)
		}
	}
	if in.p.OverlayRoot {
		return nil
	}
	tab, _, err := in.dev.Read(ctx, in.p.Inittab)
	if err != nil {
		return err
	}
	for _, f := range in.p.InittabForbidden {
		if bytes.Contains(tab, []byte(f)) {
			return fmt.Errorf("%w: %s runs something under %s", ErrRefused, in.p.Inittab, f)
		}
	}
	return nil
}

// stockPresent checks a kept stock's files are there, by their presence alone (rule 5), and its start script may be
// run
func (in *Installer) stockPresent(ctx context.Context) error {
	s := in.p.Stock
	if s == nil {
		return nil
	}
	for _, p := range append([]string{s.Dir, s.Launcher}, s.Files...) {
		if ok, err := in.dev.Exists(ctx, p); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w: %s is missing: the stock assistant this board keeps is not whole", ErrRefused, p)
		}
	}
	if ok, err := in.dev.Executable(ctx, s.Start); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s is not executable: the stock assistant could not be started again", ErrRefused, s.Start)
	}
	return nil
}

// variant adjusts the profile to the first of its variants whose facts all hold, once per run
func (in *Installer) variant(ctx context.Context) error {
	if in.variantDone {
		return nil
	}
	in.variantDone = true
	for _, v := range in.p.Variants {
		holds, err := in.factsHold(ctx, v.Facts)
		if err != nil {
			return err
		}
		if !holds {
			continue
		}
		in.p.Facts = v.Facts
		if v.Apply != nil {
			v.Apply(&in.p)
		}
		in.say("firmware variant %s", v.Name)
		return nil
	}
	return nil
}

// factsHold is every fact equal to the device's
func (in *Installer) factsHold(ctx context.Context, facts []Fact) (bool, error) {
	differ, err := in.differ(ctx, facts)
	return len(differ) == 0, err
}

// differ names the facts that differ from the device's
func (in *Installer) differ(ctx context.Context, facts []Fact) ([]string, error) {
	var differ []string
	for _, f := range facts {
		got := "missing"
		for _, p := range f.Paths {
			raw, ok, err := in.dev.Read(ctx, p)
			if err != nil {
				return nil, err
			}
			if ok {
				got = strings.TrimSpace(string(raw))
				break
			}
		}
		if got != f.Want {
			differ = append(differ, f.Name)
		}
	}
	return differ, nil
}

// firmware compares the tested firmware's facts; a difference refuses unless the owner accepted it
func (in *Installer) firmware(ctx context.Context) error {
	differ, err := in.differ(ctx, in.p.Facts)
	if err != nil {
		return err
	}
	switch {
	case len(differ) == 0:
		in.say("firmware as tested")
	case in.o.AcceptFirmware:
		in.say("firmware differs (%s): accepted", strings.Join(differ, ", "))
	default:
		return fmt.Errorf("%w: firmware differs from the tested one (%s); --accept-firmware accepts it", ErrRefused,
			strings.Join(differ, ", "))
	}
	return nil
}

// spareKB is room beside the unpacked tree, for soloist's first download among others
const spareKB = 64 << 10

// space needs the tree and, at once, the tarball while stage unpacks it or the spare once stage has removed it:
// soloist downloads only after start-v2, so the two are never on the device together
func (in *Installer) space(ctx context.Context) error {
	spare := int64(spareKB)
	if in.p.SpareKB > 0 {
		spare = in.p.SpareKB
	}
	tarKB := in.o.Bundle.Size / 1024
	need := tarKB + max(tarKB, spare)
	type check struct {
		path string
		need int64
	}
	checks := []check{{in.p.DataMount, need}, {"/", in.p.MinRootKB}}
	if in.p.StageDir != "" {
		stage, err := in.existingParent(ctx, in.p.StageDir)
		if err != nil {
			return err
		}
		// the tarball sits on the stage dir's mount, never beside the tree. a tree installed before stays beside the
		// new one until place swaps them and removes it, so the new tree needs its room at that peak and the spare
		// only once the old one is gone; v1's tree is moved aside first, never removed, so it counts for nothing
		old := int64(0)
		if in.done("v1-moved") {
			if old, err = in.dev.UsedKB(ctx, in.placed()...); err != nil {
				return err
			}
		}
		checks = []check{{in.p.DataMount, max(tarKB, tarKB+spare-old)}, {stage, tarKB}, {"/", in.p.MinRootKB}}
	}
	for _, c := range checks {
		free, err := in.dev.FreeKB(ctx, c.path)
		if err != nil {
			return err
		}
		if free < c.need {
			return fmt.Errorf("%w: %s has %d KiB free, %d needed", ErrRefused, c.path, free, c.need)
		}
	}
	return nil
}

// existingParent is the nearest ancestor of p that exists, which sits on the mount p will be created on: stage has
// not made the stage dir yet, and a dry run never does
func (in *Installer) existingParent(ctx context.Context, p string) (string, error) {
	for p != "/" && p != "." {
		if ok, err := in.dev.Exists(ctx, p); err != nil || ok {
			return p, err
		}
		p = path.Dir(p)
	}
	return "/", nil
}

// verify waits for v2: recovery access kept, v1 gone, cadenced running and linked up, and the volume at the
// persisted level
const (
	startPolls = 15
	logEvery   = 2 * time.Second
	linkWait   = 60 * time.Second
)

func (in *Installer) verify(ctx context.Context) error {
	if err := in.recovery(ctx); err != nil {
		// after the swap this is a failure to act on, not a refusal
		return fmt.Errorf("lost after the swap: %v", err) //nolint:errorlint // drops ErrRefused on purpose
	}
	procs, err := in.dev.Processes(ctx)
	if err != nil {
		return err
	}
	if left := matching(in.v1Processes(), procs); len(left) > 0 {
		return fmt.Errorf("%s runs (pid %d)", matchOf(in.v1Processes(), left[0]), left[0].Pid)
	}
	if err := in.verifyLauncher(ctx); err != nil {
		return err
	}
	if err := in.waitRunning(ctx, binary("cadenced", in.l.daemon)); err != nil {
		return err
	}
	if err := in.waitLink(ctx); err != nil {
		return err
	}
	if err := in.stockSuspended(ctx); err != nil {
		return err
	}
	if err := in.stockStaysAway(ctx); err != nil {
		return err
	}
	level, err := in.dev.Level(ctx, in.l.wrapper)
	if err != nil {
		return err
	}
	got, err := in.dev.Mixer(ctx, in.p.MixerControl)
	if err != nil {
		return err
	}
	if len(got) == 0 {
		return fmt.Errorf("%s shows no playback level", in.p.MixerControl)
	}
	for _, g := range got {
		// amixer rounds the raw value it shows back to percent
		if g < level-1 || g > level+1 {
			return fmt.Errorf("%s is at %d %%, the persisted level is %d", in.p.MixerControl, g, level)
		}
	}
	in.say("recovery access kept, cadenced linked up, %s at %d %%", in.p.MixerControl, level)
	return nil
}

// stockPolls bound the wait for a stock process to go: up to a poll gap of the wrapper (2 s), then its suspend, which
// sleeps up to 6 s and forks for every scan of /proc (a listing, a readlink per vendor pid) on a slow cpu
const stockPolls = 10

// a stock that keeps coming back between the wrapper's polls is counted returnPolls polls after it was found
// suspended; returnLimit re-suspends since cadenced started are no longer a late start
const (
	returnPolls = 3
	returnLimit = 2
)

// stockSuspended is rule 4 on a board that keeps its stock: still installed, and nothing runs from it while cadence
// does (M2m-D2), its launcher included where it lies outside its directory; a process the wrapper's next poll
// suspends is waited for
func (in *Installer) stockSuspended(ctx context.Context) error {
	s := in.p.Stock
	if s == nil {
		return nil
	}
	if ok, err := in.dev.Exists(ctx, s.Dir); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s is gone", s.Dir)
	}
	var left []Proc
	for i := range stockPolls {
		if i > 0 {
			in.o.sleep(logEvery)
		}
		procs, err := in.stockRunning(ctx)
		if err != nil {
			return err
		}
		if left = procs; len(left) == 0 {
			if in.launcherOutside() {
				in.say("nothing runs from %s, nor %s", s.Dir, s.Launcher)
			} else {
				in.say("nothing runs from %s", s.Dir)
			}
			return nil
		}
	}
	where := "from " + s.Dir
	if in.isLauncher(left[0]) && in.launcherOutside() {
		where = s.Launcher
	}
	return fmt.Errorf("pid %d runs %s: the stock assistant is not suspended", left[0].Pid, where)
}

// stockRunning is what of the stock runs: everything run from its directory and, where its launcher lies outside it
// (an overlay board's /usr/bin/voice-agent), the launcher. only the launcher is taken from its directory, which holds
// other programs
func (in *Installer) stockRunning(ctx context.Context) ([]Proc, error) {
	procs, err := in.dev.RunningFrom(ctx, in.p.Stock.Dir)
	if err != nil || !in.launcherOutside() {
		return procs, err
	}
	beside, err := in.dev.RunningFrom(ctx, path.Dir(in.p.Stock.Launcher))
	if err != nil {
		return nil, err
	}
	for _, p := range beside {
		if in.isLauncher(p) {
			procs = append(procs, p)
		}
	}
	return procs, nil
}

// launcherOutside is a stock launcher that does not lie under the stock's directory
func (in *Installer) launcherOutside() bool {
	return !strings.HasPrefix(in.p.Stock.Launcher, path.Clean(in.p.Stock.Dir)+"/")
}

// isLauncher is a process run from the stock's launcher, " (deleted)" once replaced
func (in *Installer) isLauncher(p Proc) bool {
	l := in.p.Stock.Launcher
	return p.Exe == l || p.Exe == l+" (deleted)"
}

// stockStaysAway fails a stock that keeps coming back slowly: something the suspend leaves running (its start
// script, a shell) restarts it between the wrapper's polls, which suspend it again each time, so the wrapper's log
// shows a stock_resuspended per comeback since cadenced started. one that comes back faster than the suspend ends
// keeps cadenced from starting or fails stockSuspended
func (in *Installer) stockStaysAway(ctx context.Context) error {
	s := in.p.Stock
	if s == nil {
		return nil
	}
	in.o.sleep(returnPolls * logEvery)
	n, err := in.dev.LogCount(ctx, in.l.log, "event=start", "event=stock_resuspended")
	if err != nil {
		return err
	}
	if n >= returnLimit {
		return fmt.Errorf("stock_keeps_returning: the wrapper suspended the stock again %d times since cadenced started: %s, "+
			"which the suspend leaves running, likely restarts it (an open item in AGENTS.md)", n, path.Base(s.Start))
	}
	return nil
}

func (in *Installer) waitRunning(ctx context.Context, id identity) error {
	for i := range startPolls {
		if i > 0 {
			in.o.sleep(logEvery)
		}
		procs, err := in.dev.Processes(ctx)
		if err != nil {
			return err
		}
		if len(matching([]identity{id}, procs)) > 0 {
			return nil
		}
	}
	return fmt.Errorf("%s does not run", id.name)
}

func (in *Installer) waitLink(ctx context.Context) error {
	wait := in.o.LinkWait
	if wait == 0 {
		wait = linkWait
	}
	for i := range int(wait/logEvery) + 1 {
		if i > 0 {
			in.o.sleep(logEvery)
		}
		up, err := in.dev.LogCount(ctx, in.l.log, "event=start", `"msg":"link_up"`)
		if err != nil {
			return err
		}
		if up > 0 {
			return nil
		}
	}
	return errors.New("no link_up in " + in.l.log + " within " + wait.String() + " of the start")
}
