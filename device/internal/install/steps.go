package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/config"
)

// Options are one run's inputs.
type Options struct {
	// Bundle is the checked tarball; only Install needs it
	Bundle *Bundle
	// Server and Code are enroll's, needed until the device is enrolled; Code is never printed
	Server, Code string
	// AcceptFirmware accepts firmware facts that differ from the tested ones
	AcceptFirmware bool
	// OTABlocked is the owner's word that the router blocks the vendor's OTA servers, which a board that requires it
	// needs for every install and a restore of its stock always needs
	OTABlocked bool
	// ExtraCA is the owner's own CA, as ExtraCA checked it, added to the bundle before enroll: a server behind a CA
	// of its own, as the quickstart's on a LAN
	ExtraCA []byte
	// DryRun reads the device and prints each change instead of making it
	DryRun bool
	// Recovery are the transports that answered at the start: each must still answer at the end (rule 2)
	Recovery []func(context.Context) error
	Out      io.Writer
	Now      func() time.Time
	Sleep    func(time.Duration)
	// LinkWait bounds verify's wait for link_up, 60 s when zero
	LinkWait time.Duration
}

func (o Options) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

func (o Options) sleep(d time.Duration) {
	if o.Sleep == nil {
		time.Sleep(d)
		return
	}
	o.Sleep(d)
}

// Installer runs the phases on one device. Each phase is recorded in install.state once it finished, so a run cut
// short resumes after the last one (suspend runs again while a later phase is pending); preflight runs every time and
// changes nothing.
type Installer struct {
	dev   Device
	p     Profile
	l     layout
	o     Options
	phase string
	steps []string
	// variantDone: preflight picked the firmware variant already
	variantDone bool
}

func New(dev Device, p Profile, o Options) *Installer {
	if o.Out == nil {
		o.Out = io.Discard
	}
	return &Installer{dev: dev, p: p, l: layoutOf(p.DataRoot), o: o}
}

func (in *Installer) say(format string, args ...any) {
	_, _ = fmt.Fprintf(in.o.Out, "cadence-install: %s: %s\n", in.phase, fmt.Sprintf(format, args...))
}

// phases are a swap to v2's, in order: stop-v1 not on a board that boots by an init script, which never runs v1, and
// suspend only on a board that keeps its stock; verify follows every run
func (in *Installer) phases() []string {
	var out []string
	if !in.byScript() {
		out = append(out, "stop-v1")
	}
	if in.p.Stock != nil {
		out = append(out, "suspend")
	}
	return append(out, "stage", "enroll", "start-v2")
}

// otaBlocked refuses, before anything is read, a run that needs the owner's word on the router's OTA block without it
func (in *Installer) otaBlocked(needed bool, what string) error {
	if !needed || in.o.OTABlocked {
		return nil
	}
	return fmt.Errorf("%w: %s: keep the router's rules that block the vendor's OTA servers in place, and say so with "+
		"--ota-blocked", ErrRefused, what)
}

// Install swaps the device from v1 to v2: preflight, stop-v1 (not on a board that boots by an init script), suspend
// (a board that keeps its stock), stage, enroll, start-v2, verify.
func (in *Installer) Install(ctx context.Context) error {
	if in.o.Bundle == nil {
		return fmt.Errorf("%w: no tarball", ErrRefused)
	}
	in.phase = "preflight"
	if err := in.otaBlocked(in.p.RequireOTABlocked, in.p.Board+" has no vendor OTA block of its own yet"); err != nil {
		return in.failed(err)
	}
	if err := in.preflight(ctx, true); err != nil {
		return in.failed(err)
	}
	pending, err := in.hardeningState(ctx)
	if err != nil {
		return in.failed(err)
	}
	if err := in.loadState(ctx); err != nil {
		return in.failed(err)
	}
	// once staged, the tree's room is spent
	if !in.done("stage") {
		if err := in.space(ctx); err != nil {
			return in.failed(err)
		}
	}
	if err := in.checkEnroll(ctx); err != nil {
		return in.failed(err)
	}
	if err := in.applyHardening(ctx, pending); err != nil {
		return in.failed(err)
	}
	in.say("ok")
	if err := in.dev.Mkdir(ctx, in.l.work, 0o700); err != nil {
		return in.failed(err)
	}
	run := map[string]func(context.Context) error{
		"stop-v1": in.stopV1, "suspend": in.suspend, "stage": in.stage, "enroll": in.enroll, "start-v2": in.startV2,
	}
	for _, name := range in.phases() {
		in.phase = name
		if name == "enroll" {
			if err := in.trustCA(ctx); err != nil {
				return in.failed(err)
			}
		}
		if in.skip(name) {
			in.say("done already")
			continue
		}
		if err := run[name](ctx); err != nil {
			return in.failed(err)
		}
		if err := in.mark(ctx, name); err != nil {
			return in.failed(err)
		}
		in.say("done")
	}
	in.phase = "verify"
	if in.o.DryRun {
		in.say("would check recovery access, cadenced running, link_up and the volume")
		return nil
	}
	if err := in.verify(ctx); err != nil {
		return in.failed(err)
	}
	if err := in.mark(ctx, "verify"); err != nil {
		return in.failed(err)
	}
	if err := in.dev.Remove(ctx, in.l.work); err != nil {
		return in.failed(err)
	}
	if in.byScript() {
		in.say("cadence v2 runs; cadence-install --restore-stock gives it back to its stock")
		return nil
	}
	in.say("cadence v2 runs; cadence-install --to-v1 swaps back")
	return nil
}

func (in *Installer) failed(err error) error { return fmt.Errorf("%s: %w", in.phase, err) }

// checkEnroll refuses, before anything changes, a run that would reach enroll without a code: the device would be
// left with v1 stopped and v2 unable to start
func (in *Installer) checkEnroll(ctx context.Context) error {
	if in.done("enroll") {
		return nil
	}
	if in.done("stage") {
		if ok, err := in.dev.Exists(ctx, in.l.config); err != nil || ok {
			return err
		}
	}
	if in.o.Code == "" && !in.o.DryRun {
		return fmt.Errorf("%w: the device is not enrolled yet: --code (from the dashboard) is required", ErrRefused)
	}
	if err := config.ValidateServerURL(in.p.Board, in.o.Server); err != nil || in.o.Server == "" {
		return fmt.Errorf("%w: --server must be the server's https:// url", ErrRefused)
	}
	return nil
}

func (in *Installer) done(step string) bool { return slices.Contains(in.steps, step) }

// skip is a phase install.state records as done, but suspend while a phase after it is still to run: a reboot between
// runs lets the stock's init script start it again, and suspend may run any number of times
func (in *Installer) skip(phase string) bool {
	if phase == "suspend" && (!in.done("stage") || !in.done("enroll") || !in.done("start-v2")) {
		return false
	}
	return in.done(phase)
}

func (in *Installer) loadState(ctx context.Context) error {
	raw, _, err := in.dev.Read(ctx, in.l.state)
	if err != nil {
		return err
	}
	in.steps = nil
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			in.steps = append(in.steps, l)
		}
	}
	return nil
}

// mark records a finished step in install.state; a dry run only prints the write, so later steps still see the
// device as it is
func (in *Installer) mark(ctx context.Context, step string) error {
	steps := in.steps
	if !in.done(step) {
		steps = append(slices.Clone(steps), step)
	}
	if !in.o.DryRun {
		in.steps = steps
	}
	return in.dev.Write(ctx, in.l.state, []byte(strings.Join(steps, "\n")+"\n"), 0o600)
}

// workWrapper writes the tarball's wrapper into the work dir, with its board's hook beside it, where the wrapper
// sources it from, and returns the wrapper's path
func (in *Installer) workWrapper(ctx context.Context) (string, error) {
	if err := in.dev.Write(ctx, path.Join(in.l.work, "board-hook"), in.o.Bundle.Hook, 0o644); err != nil {
		return "", err
	}
	wrapper := path.Join(in.l.work, "cadence-run")
	return wrapper, in.dev.Write(ctx, wrapper, in.o.Bundle.Wrapper, 0o755)
}

// stopV1 takes v1 out of init and stops it, data kept (M2-D1): a held v1 would go on setting the volume
func (in *Installer) stopV1(ctx context.Context) error {
	wrapper, err := in.workWrapper(ctx)
	if err != nil {
		return err
	}
	tab, _, err := in.readInittab(ctx)
	if err != nil {
		return err
	}
	had := hasEntry(tab, in.l.v1Entry)
	if err := in.setInittab(ctx, "stop-v1", in.l.v1Entry, func(cur []byte) ([]byte, error) {
		return withoutEntry(cur, in.l.v1Entry), nil
	}); err != nil {
		return err
	}
	if had {
		in.say("init no longer runs %s", in.l.v1Init)
	}
	if err := in.stop(ctx, in.v1Processes(), in.l.v1Pidfiles); err != nil {
		return err
	}
	in.blank(ctx, wrapper)
	return nil
}

// suspend stops the stock this board keeps before anything of v2 is staged, through the tarball's wrapper and hook in
// the work dir, the same suspend as every start of cadenced (M2m-D2): a stock that will not stop ends the run here,
// recovery access untouched, and a later run resumes at this phase
func (in *Installer) suspend(ctx context.Context) error {
	wrapper, err := in.workWrapper(ctx)
	if err != nil {
		return err
	}
	if err := in.dev.Prestart(ctx, wrapper); err != nil {
		return err
	}
	in.say("the stock assistant is suspended")
	return nil
}

func (in *Installer) v1Processes() []identity {
	return []identity{
		script("v1's supervisor", in.l.v1Init), binary("v1's cadenced", in.l.v1Daemon), binary("v1's media helper", in.l.v1Media),
	}
}

func (in *Installer) v2Processes() []identity {
	return []identity{script("cadence-run", in.l.wrapper), binary("cadenced", in.l.daemon), binary("cadence-audiod", in.l.audiod)}
}

// blank turns the lights off; a failure leaves them as they are and is only reported: the next start blanks them
func (in *Installer) blank(ctx context.Context, wrapper string) {
	if err := in.dev.Blank(ctx, wrapper); err != nil {
		in.say("the lights could not be blanked: %v", err)
	}
}

// stage moves v1's tree aside, converts the owner's volume and puts v2's tree in place from the tarball
func (in *Installer) stage(ctx context.Context) (err error) {
	if !in.done("v1-moved") {
		n, err := in.moveEntries(ctx, in.l.root, in.l.v1Dir, kept)
		if err != nil {
			return err
		}
		if err := in.mark(ctx, "v1-moved"); err != nil {
			return err
		}
		in.say("moved %d entries of v1 to %s", n, in.l.v1Dir)
	}
	if err := in.convertVolume(ctx); err != nil {
		return err
	}
	tarball := path.Join(in.l.work, "cadence.tar")
	if in.p.StageDir != "" {
		tarball = path.Join(in.p.StageDir, "cadence.tar")
		if err := in.dev.Mkdir(ctx, in.p.StageDir, 0o700); err != nil {
			return err
		}
	}
	if err := in.pushBundle(ctx, tarball); err != nil {
		return err
	}
	tree := path.Join(in.l.work, "tree")
	// a leftover tree (a crash, power loss) is scratch: stage extracts it again
	if err := in.dev.Remove(ctx, tree); err != nil {
		return err
	}
	// until the first entry is placed the tree is scratch on every failure path, and a resume's space check
	// counts it; once placing starts it stays as it was
	placing := false
	defer func() {
		if err != nil && !placing {
			_ = in.dev.Remove(context.WithoutCancel(ctx), tree)
		}
	}()
	if err := in.dev.Unpack(ctx, tarball, tree); err != nil {
		return err
	}
	if err := in.dev.Check(ctx, tree, path.Join(tree, "MANIFEST"), path.Join(in.l.work, "sums")); err != nil {
		return err
	}
	for _, top := range in.o.Bundle.Top {
		placing = true
		from, to := path.Join(tree, top), in.placedAt(top)
		if bundleRuntimes[top] {
			if err := in.dev.Mkdir(ctx, path.Dir(to), 0o700); err != nil {
				return err
			}
			from = path.Join(from, "runtime")
		}
		if err := in.place(ctx, from, to); err != nil {
			return err
		}
	}
	in.say("cadence %s is in %s", in.o.Bundle.Version, in.l.root)
	if err := in.dev.Remove(ctx, tree); err != nil {
		return err
	}
	return in.dev.Remove(ctx, tarball)
}

// placedAt is where stage puts one of the tarball's top entries, a runtime's one level down
func (in *Installer) placedAt(top string) string {
	if bundleRuntimes[top] {
		return path.Join(in.l.root, top, "runtime")
	}
	return path.Join(in.l.root, top)
}

// placed are the entries stage puts in place, which replace those an earlier install put there
func (in *Installer) placed() []string {
	out := make([]string, len(in.o.Bundle.Top))
	for i, top := range in.o.Bundle.Top {
		out[i] = in.placedAt(top)
	}
	return out
}

// unmoved is a dry run on a device whose v1 tree is still in place, where a real run would have moved it: what is
// in the data root then is v1's
func (in *Installer) unmoved() bool { return in.o.DryRun && !in.done("v1-moved") }

// convertVolume writes v2's state/volume once, from v1's, and none when v1's cannot be read
func (in *Installer) convertVolume(ctx context.Context) error {
	src := path.Join(in.l.v1Dir, "state", "volume")
	if in.unmoved() {
		src = in.l.volume
	} else if ok, err := in.dev.Exists(ctx, in.l.volume); err != nil || ok {
		return err
	}
	raw, _, err := in.dev.Read(ctx, src)
	if err != nil {
		return err
	}
	data, level, ok := toV2(raw)
	if !ok {
		in.say("no volume: v1's could not be read, so v2 starts at its default with the level unknown")
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

// pushBundle pushes the tarball unless the device has it already, and refuses one whose sha256 differs from the
// checked tarball's
func (in *Installer) pushBundle(ctx context.Context, tarball string) error {
	b := in.o.Bundle
	if s, ok, err := in.dev.Sum(ctx, tarball); err != nil || (ok && s == b.Sum) {
		return err
	}
	if err := in.dev.Push(ctx, b.Path, tarball); err != nil {
		return err
	}
	in.say("pushed %s (%d bytes)", path.Base(b.Path), b.Size)
	if in.o.DryRun {
		return nil
	}
	if s, ok, err := in.dev.Sum(ctx, tarball); err != nil || !ok || s != b.Sum {
		_ = in.dev.Remove(context.WithoutCancel(ctx), tarball)
		return errors.Join(fmt.Errorf("%w: the tarball's sha256 on the device differs from %s", ErrRefused, b.Sum), err)
	}
	return nil
}

// place swaps from in at to, the old one moved aside first and removed last
func (in *Installer) place(ctx context.Context, from, to string) error {
	old := to + ".old"
	if err := in.dev.Remove(ctx, old); err != nil {
		return err
	}
	if ok, err := in.dev.Exists(ctx, to); err != nil {
		return err
	} else if ok && !in.unmoved() {
		if err := in.dev.Rename(ctx, to, old); err != nil {
			return err
		}
	}
	if err := in.dev.Rename(ctx, from, to); err != nil {
		return err
	}
	return in.dev.Remove(ctx, old)
}

// enroll trades the dashboard's code for the device's token; cadenced writes the config, mode 600
func (in *Installer) enroll(ctx context.Context) error {
	if ok, err := in.dev.Exists(ctx, in.l.config); err != nil {
		return err
	} else if ok && !in.unmoved() {
		in.say("enrolled already: %s kept", in.l.config)
		return nil
	}
	if in.o.Code == "" {
		if in.o.DryRun {
			in.say("would enroll, which needs --code")
			return nil
		}
		return fmt.Errorf("%w: --code is required", ErrRefused)
	}
	code := path.Join(in.l.work, "code")
	defer func() { _ = in.dev.Remove(context.WithoutCancel(ctx), code) }()
	if err := in.dev.WriteSecret(ctx, code, []byte(in.o.Code)); err != nil {
		return err
	}
	if err := in.dev.Enroll(ctx, Enrollment{
		Daemon: in.l.daemon, Server: in.o.Server, CodeFile: code, Board: in.p.Board, DataDir: in.l.root, CAFile: in.l.ca,
		Config: in.l.config,
	}); err != nil {
		return err
	}
	in.say("enrolled")
	return nil
}
