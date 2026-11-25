package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/install/busyboxtest"
)

// an overlay board: / is an rw overlay over a read-only lower layer, so cadence boots through the stock assistant's
// init script, whose stock copy stays readable in the lower layer
const (
	initScript  = "/etc/init.d/S97voice-agent.sh"
	initLower   = "/rom/etc/init.d/S97voice-agent.sh"
	initPidFile = "/tmp/cadence/cadence-run.pid"
	okcallInit  = "/etc/init.d/S97okcall.sh"
	syncerInit  = "/etc/init.d/S51ntp-ibb-volume-syncer"
	// the overlay board's stock launcher lies outside its directory, as /usr/bin/voice-agent does
	outsideLauncher = "/usr/bin/stock-launcher"
)

var (
	launcherBytes = []byte("#!/bin/sh\n# cadence's launcher\n")
	stockS97      = []byte("#!/bin/sh\n# the stock's S97\n")
)

func testOverlayProfile() Profile {
	p := testMidiProfile()
	p.Board, p.Arch = "vk-capsula-mini", "arm64"
	p.Inittab, p.Recovery, p.Anchor, p.InittabForbidden = "", nil, "", nil
	p.RecoveryDaemons = []string{"dropbearmulti"}
	p.Stock = &Stock{Dir: stockDir, Start: initLower, Launcher: outsideLauncher, Files: []string{stockLibrary}}
	p.OverlayRoot, p.InitScript, p.InitScriptLower, p.InitLauncher = true, initScript, initLower, launcherBytes
	p.InitPidFile, p.BootPath = initPidFile, "/usr/sbin:/usr/bin:/sbin:/bin"
	p.PlatformScripts = []PlatformScript{{okcallInit, "okcall"}, {syncerInit, "ntp-ibb-volume-syncer"}}
	p.RequireOTABlocked = false
	return p
}

// overlaySpeaker is an overlay board as its firmware boots it: no inittab of ours, the stock and the platform's
// processes running from their init scripts, recovery access from a daemon that is not init's
func overlaySpeaker(t *testing.T) *fake {
	f := newFake(t, testOverlayProfile())
	f.put("/proc/device-tree/model", []byte("Test Station\x00"), 0o444)
	f.put("/proc/sys/kernel/osrelease", []byte("4.9.68\n"), 0o444)
	f.put(stockDir+"/VERSION", []byte("3.21\n"), 0o644)
	f.put(outsideLauncher, []byte("elf"), 0o755)
	f.put(stockLibrary, []byte("elf"), 0o644)
	f.put(initLower, stockS97, 0o755)
	// the merged path shows the lower copy until something writes it
	f.put(initScript, stockS97, 0o755)
	for _, s := range f.p.PlatformScripts {
		f.put(s.Script, []byte("#!/bin/sh\n"), 0o755)
		f.spawn(Proc{Exe: "/usr/bin/" + s.Process, Args: []string{s.Process}}, "", 0)
	}
	f.spawn(Proc{Exe: "/usr/lib/capsula-root/bin/dropbearmulti", Args: []string{"dropbearmulti"}}, "", 0)
	f.startStock()
	return f
}

// platformPids counts each platform process running
func platformPids(f *fake) []int {
	var n []int
	for _, s := range f.p.PlatformScripts {
		n = append(n, len(f.pidsOf("/usr/bin/"+s.Process)))
	}
	return n
}

func overlayOpts(t *testing.T, f *fake) Options {
	t.Helper()
	o := runOpts(t, f)
	headers := []string{"cadence 72407e22cafd", "board vk-capsula-mini", "arch arm64", "go go1.27.3"}
	b, err := OpenBundle(writeBundle(t, bundleFiles(), headers, nil), "vk-capsula-mini", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	o.Bundle = b
	return o
}

func mustInstallOverlay(t *testing.T, f *fake) string {
	t.Helper()
	out, err := install(t, f, overlayOpts(t, f))
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	return out
}

func restoreOverlayOpts(t *testing.T, f *fake) Options {
	o := overlayOpts(t, f)
	o.Bundle = nil
	return o
}

func noInittabOps(t *testing.T, ops []string) {
	t.Helper()
	for _, op := range ops {
		for _, banned := range []string{"swap-inittab", "remount", "hup", "inittab"} {
			if strings.Contains(op, banned) {
				t.Fatalf("an overlay board's run touched the inittab or the root: %q in:\n%s", op, strings.Join(ops, "\n"))
			}
		}
	}
}

// install writes the launcher over the stock's init script and starts cadence through it: no inittab, no remount,
// no stop-v1, and the stock handed over by cadence-run's own prestart
func TestInitScriptInstallWritesLauncherAndStarts(t *testing.T) {
	f := overlaySpeaker(t)
	out := mustInstallOverlay(t, f)
	ops := f.changes()
	noInittabOps(t, ops)
	i := slices.Index(ops, "cp /data/cadence/share/board-hook /data/cadence/bin/board-hook 644")
	want := []string{
		"cp /data/cadence/share/board-hook /data/cadence/bin/board-hook 644",
		"cp /data/cadence/share/cadence-run /data/cadence/bin/cadence-run 755",
		"write-file " + initScript + " 0755",
		"run-script " + initScript + " start",
		"write /data/cadence/install.state 600",
	}
	if i < 0 || len(ops) < i+len(want) || !slices.Equal(ops[i:i+len(want)], want) {
		t.Fatalf("start-v2's commands:\n%s", strings.Join(ops, "\n"))
	}
	if slices.Contains(ops, "run-script "+initScript+" stop") || slices.ContainsFunc(ops, func(o string) bool { return strings.HasPrefix(o, "kill") }) {
		t.Fatalf("stopped something with nothing of cadence running:\n%s", strings.Join(ops, "\n"))
	}
	if got := string(f.get("/data/cadence/install.state")); got != "suspend\nv1-moved\nstage\nenroll\nstart-v2\nverify\n" {
		t.Fatalf("install.state %q", got)
	}
	if !bytes.Equal(f.get(initScript), launcherBytes) || f.files[initScript].mode != 0o755 || !bytes.Equal(f.get(initLower), stockS97) {
		t.Fatal("the launcher is not at the init script, or the lower copy changed")
	}
	if f.supervisor() == 0 || f.feeder == 0 || len(f.pidsOf("/data/cadence/bin/cadenced")) != 1 || len(f.stockPids()) != 0 ||
		!slices.Equal(platformPids(f), []int{0, 0}) {
		t.Fatalf("processes: %+v", f.procs)
	}
	if !strings.Contains(out, "verify: recovery access kept, cadenced linked up, Master at 30 %") ||
		!strings.Contains(out, "cadence-install --restore-stock gives it back to its stock") || strings.Contains(out, "--to-v1") {
		t.Fatalf("output:\n%s", out)
	}
}

// a start-v2 that finds cadence-run running stops it through the launcher before starting it again, though its
// feeder, with the same command line, outlives it through the hook's grace; a launcher that is in place already is
// not written again
func TestInitScriptUpgradeRestartsThroughLauncher(t *testing.T) {
	f := overlaySpeaker(t)
	mustInstallOverlay(t, f)
	old, oldFeeder := f.supervisor(), f.feeder
	f.put("/data/cadence/install.state", []byte("suspend\nv1-moved\nstage\nenroll\n"), 0o600)
	f.ops = nil
	mustInstallOverlay(t, f)
	ops := f.changes()
	noInittabOps(t, ops)
	cp := slices.Index(ops, "cp /data/cadence/share/cadence-run /data/cadence/bin/cadence-run 755")
	stop := slices.Index(ops, "run-script "+initScript+" stop")
	start := slices.Index(ops, "run-script "+initScript+" start")
	if cp < 0 || stop < cp || start < stop {
		t.Fatalf("commands:\n%s", strings.Join(ops, "\n"))
	}
	if slices.ContainsFunc(ops, func(o string) bool { return strings.HasPrefix(o, "write-file") }) {
		t.Fatalf("the launcher was written again:\n%s", strings.Join(ops, "\n"))
	}
	if now := f.supervisor(); now == 0 || now == old || f.feeder == oldFeeder || len(f.wrapperPids()) != 2 {
		t.Fatalf("cadence-run before %d, after %d; feeder before %d, after %d; %v", old, now, oldFeeder, f.feeder, f.wrapperPids())
	}
}

// verify needs the cadence-run the pid file names: its feeder, in grace with the same command line, is not it
func TestInitScriptVerifyNeedsTheSupervisorNotItsFeeder(t *testing.T) {
	f := overlaySpeaker(t)
	mustInstallOverlay(t, f)
	f.launcher("stop")
	f.put("/data/cadence/disabled", nil, 0o644)
	f.put("/data/cadence/install.state", []byte("suspend\nv1-moved\nstage\nenroll\n"), 0o600)
	if f.feeder == 0 || f.supervisor() != 0 {
		t.Fatalf("setup: feeder %d, supervisor %d", f.feeder, f.supervisor())
	}
	_, err := install(t, f, overlayOpts(t, f))
	if err == nil || !strings.Contains(err.Error(), "verify: cadence-run does not run") {
		t.Fatalf("err = %v", err)
	}
}

// verify looks for the stock's launcher where it lies, outside its directory: one that survives beside cadence-run
// fails the install (an overlay board's /usr/bin/voice-agent), and nothing of the stock's directory runs
func TestInitScriptVerifyFindsTheLauncherOutsideItsDir(t *testing.T) {
	f := overlaySpeaker(t)
	f.started = func(f *fake) {
		if len(f.pidsOf(outsideLauncher)) == 0 {
			f.spawn(Proc{Exe: outsideLauncher, Args: []string{outsideLauncher}}, "", 0)
		}
	}
	_, err := install(t, f, overlayOpts(t, f))
	if err == nil || errors.Is(err, ErrRefused) || !strings.HasPrefix(err.Error(), "verify: ") ||
		!strings.Contains(err.Error(), "runs "+outsideLauncher+": the stock assistant is not suspended") {
		t.Fatalf("err = %v", err)
	}
	if len(f.pidsOf(stockDir+"/maind")) != 0 {
		t.Fatalf("the stock's directory runs: %v", f.pidsOf(stockDir+"/maind"))
	}
}

// a cadence-run that does not go within 10 s of the launcher's stop fails the run: nothing is started beside it, and
// nothing is killed, as its hook may still be feeding the watchdog
func TestInitScriptStopThatTimesOutStartsNothing(t *testing.T) {
	f := overlaySpeaker(t)
	mustInstallOverlay(t, f)
	f.put("/data/cadence/install.state", []byte("suspend\nv1-moved\nstage\nenroll\n"), 0o600)
	f.stubborn["/bin/busybox"] = true
	f.ops = nil
	o := overlayOpts(t, f)
	var slept time.Duration
	o.Sleep = func(d time.Duration) { slept += d }
	_, err := install(t, f, o)
	if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "start-v2: cadence-run (pid") ||
		!strings.Contains(err.Error(), "still runs 10s after") {
		t.Fatalf("err = %v", err)
	}
	if slept != 10*time.Second {
		t.Fatalf("waited %s", slept)
	}
	ops := f.changes()
	if slices.Contains(ops, "run-script "+initScript+" start") || slices.ContainsFunc(ops, func(o string) bool { return strings.HasPrefix(o, "kill") }) {
		t.Fatalf("commands:\n%s", strings.Join(ops, "\n"))
	}
}

// readSpy records the reads too, so a test can see that nothing at all ran between two commands
type readSpy struct{ *fake }

func (s readSpy) note(what string) { s.ops = append(s.ops, "read "+what) }

func (s readSpy) Read(ctx context.Context, p string) ([]byte, bool, error) {
	s.note(p)
	return s.fake.Read(ctx, p)
}

func (s readSpy) Exists(ctx context.Context, p string) (bool, error) {
	s.note(p)
	return s.fake.Exists(ctx, p)
}

func (s readSpy) Sum(ctx context.Context, p string) (string, bool, error) {
	s.note(p)
	return s.fake.Sum(ctx, p)
}

func (s readSpy) Processes(ctx context.Context) ([]Proc, error) {
	s.note("processes")
	return s.fake.Processes(ctx)
}

func (s readSpy) Running(ctx context.Context, name string) (bool, error) {
	s.note(name)
	return s.fake.Running(ctx, name)
}

func (s readSpy) RunningFrom(ctx context.Context, dir string) ([]Proc, error) {
	s.note(dir)
	return s.fake.RunningFrom(ctx, dir)
}

func (s readSpy) Executable(ctx context.Context, p string) (bool, error) {
	s.note(p)
	return s.fake.Executable(ctx, p)
}

func (s readSpy) List(ctx context.Context, dir string) ([]string, error) {
	s.note(dir)
	return s.fake.List(ctx, dir)
}

// --restore-stock on an overlay board: cadence-run stopped through the launcher (its feeder feeding on through the
// grace), the lower copy's bytes written back through the merged path (never removed there: that would leave a
// whiteout), then the feeder released and the stock's start run in one command, so the stock opens the watchdog
// within its timeout; every read comes before it, so only that write sits between them. then each platform script
// whose process is not running
func TestInitScriptRestoreWritesLowerBackThenHandsOver(t *testing.T) {
	f := overlaySpeaker(t)
	mustInstallOverlay(t, f)
	f.ops = nil
	f.fail["run-script "+okcallInit] = errors.New("S97okcall.sh start failed (status 1)")
	var out bytes.Buffer
	o := restoreOverlayOpts(t, f)
	o.Out = &out
	if err := New(readSpy{f}, f.p, o).RestoreStock(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	all := f.changes()
	hand := slices.Index(all, "hand-over /data/cadence/bin/cadence-run "+initScript)
	if hand < 2 || all[hand-1] != "write-file "+initScript+" 0755" || !strings.HasPrefix(all[hand-2], "read ") {
		t.Fatalf("something but the lower copy's write ran between the last read and the hand-over:\n%s", strings.Join(all, "\n"))
	}
	var ops []string
	for _, op := range all {
		if !strings.HasPrefix(op, "read ") {
			ops = append(ops, op)
		}
	}
	noInittabOps(t, ops)
	want := []string{
		"run-script " + initScript + " stop",
		"blank /data/cadence/bin/cadence-run",
		"write /data/cadence/install.state 600",
		"write-file " + initScript + " 0755",
		"hand-over /data/cadence/bin/cadence-run " + initScript,
		"run-script " + okcallInit + " start",
		"run-script " + syncerInit + " start",
		"rm /data/cadence/.install",
	}
	if ops[0] == "mkdir /data/cadence/.install 700" {
		ops = ops[1:]
	}
	if !slices.Equal(ops, want) {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(ops, "\n"), strings.Join(want, "\n"))
	}
	if !bytes.Equal(f.get(initScript), stockS97) || f.files[initScript].mode != 0o755 || !bytes.Equal(f.get(initLower), stockS97) {
		t.Fatal("the init script is not the stock's again")
	}
	if f.feeder != 0 || len(f.wrapperPids()) != 0 || len(f.pidsOf("/data/cadence/bin/cadenced")) != 0 ||
		len(f.pidsOf(outsideLauncher)) != 1 || !slices.Equal(platformPids(f), []int{0, 1}) {
		t.Fatalf("processes: %+v", f.procs)
	}
	if got := string(f.get("/data/cadence/install.state")); got != "suspend\nv1-moved\nstage\nenroll\n" {
		t.Fatalf("install.state %q", got)
	}
	for _, s := range []string{okcallInit + " did not start", "started " + syncerInit, "verify: recovery access kept, the stock assistant runs"} {
		if !strings.Contains(out.String(), s) {
			t.Fatalf("output lacks %q:\n%s", s, out.String())
		}
	}

	// again: the stock runs and the script is its own, so neither is touched, the stock's stop never runs, nothing
	// is released, and only the platform script whose process is not running is started, once
	f.ops = nil
	delete(f.fail, "run-script "+okcallInit)
	if out, err := restore(t, f, restoreOverlayOpts(t, f)); err != nil || !strings.Contains(out, "runs already") {
		t.Fatalf("%v\n%s", err, out)
	}
	ops = f.changes()
	if !slices.Contains(ops, "run-script "+okcallInit+" start") || !slices.Equal(platformPids(f), []int{1, 1}) {
		t.Fatalf("a second run: %q, platform %v", ops, platformPids(f))
	}
	for _, op := range ops {
		if strings.HasPrefix(op, "run-script "+initScript) || strings.HasPrefix(op, "write-file") || strings.HasPrefix(op, "hand-over") ||
			op == "run-script "+syncerInit+" start" {
			t.Fatalf("a second run changed: %q", ops)
		}
	}

	// and a cadence-run the pid file names while the script is the stock's is not stopped through it
	f.put(initPidFile, []byte(fmt.Sprintf("%d\n", f.runV2(""))), 0o644)
	f.ops = nil
	if _, err := restore(t, f, restoreOverlayOpts(t, f)); err == nil || !strings.Contains(err.Error(), "its stop would stop the stock") {
		t.Fatalf("err = %v", err)
	}
	if slices.ContainsFunc(f.changes(), func(o string) bool { return strings.HasPrefix(o, "run-script") || strings.HasPrefix(o, "hand-over") }) {
		t.Fatalf("commands: %q", f.changes())
	}
}

// a restore that finds no bin/cadence-run has no hook to release and starts the stock alone
func TestInitScriptRestoreWithoutTheWrapperStartsTheStock(t *testing.T) {
	f := overlaySpeaker(t)
	mustInstallOverlay(t, f)
	f.launcher("stop")
	f.exit(f.feeder)
	f.feeder = 0
	delete(f.files, "/data/cadence/bin/cadence-run")
	f.ops = nil
	if out, err := restore(t, f, restoreOverlayOpts(t, f)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ops := f.changes()
	if !slices.Contains(ops, "run-script "+initScript+" start") || slices.ContainsFunc(ops, func(o string) bool { return strings.HasPrefix(o, "hand-over") }) {
		t.Fatalf("commands:\n%s", strings.Join(ops, "\n"))
	}
	if !bytes.Equal(f.get(initScript), stockS97) || len(f.pidsOf(outsideLauncher)) != 1 {
		t.Fatalf("the stock is not back: %+v", f.procs)
	}
}

// the stock's launcher may lie outside its directory (an overlay board's /usr/bin/voice-agent): restore finds it
// running where it lies, on either boot
func TestRestoreStockFindsALauncherOutsideItsDir(t *testing.T) {
	f := midiSpeaker(t, true)
	f.p.Stock.Launcher = outsideLauncher
	f.put(outsideLauncher, []byte("elf32"), 0o755)
	f.suspendStock()
	f.startStock()
	mustInstallMidi(t, f)
	if out, err := restore(t, f, restoreOpts(t, f)); err != nil || !strings.Contains(out, "verify: recovery access kept, the stock assistant runs") {
		t.Fatalf("%v\n%s", err, out)
	}
	f.ops = nil
	if out, err := restore(t, f, restoreOpts(t, f)); err != nil || !strings.Contains(out, outsideLauncher+" runs already") {
		t.Fatalf("a second run: %v\n%s", err, out)
	}
	if slices.ContainsFunc(f.changes(), func(o string) bool { return strings.HasPrefix(o, "start-stop-daemon") }) || len(f.pidsOf(outsideLauncher)) != 1 {
		t.Fatalf("a second stock: %q", f.changes())
	}
}

// a board that boots by an init script never ran v1, so there is nothing for --to-v1 to go back to
func TestInitScriptRefusesToV1(t *testing.T) {
	f := overlaySpeaker(t)
	mustInstallOverlay(t, f)
	f.ops = nil
	_, err := rollback(t, f, restoreOverlayOpts(t, f))
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "--restore-stock") {
		t.Fatalf("err = %v", err)
	}
	if len(f.changes()) != 0 {
		t.Fatalf("a refused run changed: %q", f.changes())
	}
}

// an install refuses a v1 tree on a board that boots by an init script, in place or moved aside
func TestInitScriptInstallRefusesAV1Tree(t *testing.T) {
	for _, p := range []string{"/data/cadence/cadenced.init", "/data/cadence/v1/cadenced.init"} {
		f := overlaySpeaker(t)
		f.put(p, []byte("#!/bin/sh\n"), 0o755)
		_, err := install(t, f, overlayOpts(t, f))
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "v1") {
			t.Fatalf("%s: err = %v", p, err)
		}
		if len(f.changes()) != 0 {
			t.Fatalf("%s: a refused run changed: %q", p, f.changes())
		}
	}
}

// a profile that would boot half by the inittab and half by an init script is refused before anything is read
func TestOverlayProfileBootsOneWay(t *testing.T) {
	for name, change := range map[string]func(*Profile){
		"overlay without a script": func(p *Profile) { p.InitScript = "" },
		"script without overlay":   func(p *Profile) { p.OverlayRoot = false },
		"no lower copy":            func(p *Profile) { p.InitScriptLower = "" },
		"no launcher":              func(p *Profile) { p.InitLauncher = nil },
		"no pid file":              func(p *Profile) { p.InitPidFile = "" },
		"an inittab too":           func(p *Profile) { p.Inittab = "/etc/inittab" },
	} {
		t.Run(name, func(t *testing.T) {
			f := overlaySpeaker(t)
			change(&f.p)
			_, err := install(t, f, overlayOpts(t, f))
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "profile") {
				t.Fatalf("err = %v", err)
			}
			if len(f.changes()) != 0 {
				t.Fatalf("a refused run changed: %q", f.changes())
			}
		})
	}
}

// --restore-stock asks for --ota-blocked only on a board that requires it: one with its own OTA block restores
// without the owner's word, on either boot
func TestRestoreStockHonoursRequireOTABlocked(t *testing.T) {
	f := overlaySpeaker(t)
	mustInstallOverlay(t, f)
	if out, err := restore(t, f, restoreOverlayOpts(t, f)); err != nil {
		t.Fatalf("overlay: %v\n%s", err, out)
	}

	f = midiSpeaker(t, true)
	mustInstallMidi(t, f)
	f.p.RequireOTABlocked = false
	o := restoreOpts(t, f)
	o.OTABlocked = false
	if out, err := restore(t, f, o); err != nil {
		t.Fatalf("midi without the requirement: %v\n%s", err, out)
	}

	f = overlaySpeaker(t)
	f.p.RequireOTABlocked = true
	o = overlayOpts(t, f)
	o.OTABlocked = true
	if out, err := install(t, f, o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	f.ops = nil
	if _, err := restore(t, f, restoreOverlayOpts(t, f)); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "--ota-blocked") {
		t.Fatalf("err = %v", err)
	}
	if len(f.changes()) != 0 {
		t.Fatalf("a refused run changed: %q", f.changes())
	}
}

// tamper is a transport that changes what it pushes, as a broken transfer would
type tamper struct{ box }

func (t tamper) Push(ctx context.Context, local, p string) error {
	if err := t.box.Push(ctx, local, p); err != nil {
		return err
	}
	if out, _, code := t.c.Sh("echo x >> "+quote(p), nil); code != 0 {
		return fmt.Errorf("tamper: %s", out)
	}
	return nil
}

func TestWriteFileOnBusybox(t *testing.T) {
	c := busyboxtest.Start(t)
	c.Put("/fake/etc/init.d/S97voice-agent.sh", stockS97, "644")
	ctx := context.Background()
	inode := func() string {
		return strings.TrimSpace(string(c.MustSh("stat -c %i /fake/etc/init.d/S97voice-agent.sh")))
	}
	before := inode()

	s := NewShell(box{c}, ShellOptions{Root: fakeRoot})
	if err := s.WriteFile(ctx, initScript, launcherBytes, "0755"); err != nil {
		t.Fatal(err)
	}
	if got := string(c.MustSh("cd /fake/etc/init.d && stat -c '%a' S97voice-agent.sh && cat S97voice-agent.sh && ls -a")); got !=
		"755\n"+string(launcherBytes)+".\n..\nS97voice-agent.sh\n" {
		t.Fatalf("after the write:\n%s", got)
	}
	// renamed into place, never rewritten in place
	written := inode()
	if written == before {
		t.Fatal("the file was rewritten in place, not renamed over")
	}

	err := NewShell(tamper{box{c}}, ShellOptions{Root: fakeRoot}).WriteFile(ctx, initScript, stockS97, "0644")
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "arrived different") {
		t.Fatalf("a tampered stage: err = %v", err)
	}
	if got := string(c.MustSh("cd /fake/etc/init.d && stat -c '%a' S97voice-agent.sh && cat S97voice-agent.sh && ls -a")); got !=
		"755\n"+string(launcherBytes)+".\n..\nS97voice-agent.sh\n" || inode() != written {
		t.Fatalf("a refused write changed the target or left its stage:\n%s", got)
	}

	for _, bad := range []string{"755; rm -rf /", "u+x", "", "08"} {
		if err := s.WriteFile(ctx, initScript, stockS97, bad); err == nil {
			t.Fatalf("mode %q accepted", bad)
		}
	}
}

// breakCopy changes the staged copy after cp made it, as a broken copy would
type breakCopy struct {
	box
	tmp string
}

func (b breakCopy) Run(ctx context.Context, script string) (Result, error) {
	return b.box.Run(ctx, strings.Replace(script, "&& h=$(", "&& echo x >> "+b.tmp+" && h=$(", 1))
}

func TestCopyFileOnBusybox(t *testing.T) {
	c := busyboxtest.Start(t)
	big := bytes.Repeat([]byte("stock fwupdater\n"), 20000)
	c.Put("/fake/rom/fwupdater", big, "755")
	c.Put("/fake/usr/bin/fwupdater", []byte("#!/bin/sh\n"), "644")
	ctx := context.Background()
	state := func() string {
		return string(c.MustSh("cd /fake/usr/bin && stat -c '%a %i' fwupdater && ls -a && sha256sum < fwupdater"))
	}

	s := NewShell(box{c}, ShellOptions{Root: fakeRoot})
	before := state()
	err := NewShell(breakCopy{box{c}, "/fake/usr/bin/.fwupdater.cadence-new"}, ShellOptions{Root: fakeRoot}).
		CopyFile(ctx, "/rom/fwupdater", "/usr/bin/fwupdater", "0755")
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "copied different") {
		t.Fatalf("a broken copy: err = %v", err)
	}
	if got := state(); got != before {
		t.Fatalf("a refused copy changed the target or left its stage:\n%s\nwas\n%s", got, before)
	}

	if err := s.CopyFile(ctx, "/rom/fwupdater", "/usr/bin/fwupdater", "0755"); err != nil {
		t.Fatal(err)
	}
	want := sum(big)
	if got := string(c.MustSh("cd /fake/usr/bin && stat -c %a fwupdater && ls -a && sha256sum < fwupdater")); !strings.HasPrefix(got, "755\n.\n..\nfwupdater\n"+want) {
		t.Fatalf("after the copy:\n%s", got)
	}
	if len(big) <= maxRead {
		t.Fatal("the file is not past the read cap")
	}
	if err := s.CopyFile(ctx, "/rom/fwupdater", "/usr/bin/fwupdater", "755; rm -rf /"); err == nil {
		t.Fatal("bad mode accepted")
	}
}

// the init scripts and the wrapper's release run as init runs them: input /dev/null and, with a boot PATH, its PATH
// rather than the session's; the hand-over's start runs whether or not the release worked
func TestInitScriptCommandsOnBusybox(t *testing.T) {
	c := busyboxtest.Start(t)
	c.Tree(map[string]string{
		"/fake/etc/init.d/S97x.sh": `echo "arg=$1 path=$PATH"; read line && echo "stdin=$line"; echo err >&2; ` +
			`echo "$1" >> /fake/log/script; [ ! -e /fake/start-fails ] || exit 7`,
		"/fake/data/cadence/bin/cadence-run": "#!/bin/sh\ncase $1 in release) echo \"release path=$PATH\" >> /fake/log/release; " +
			"[ ! -e /fake/release-fails ] || { echo 'cadence-run: event=watchdog_release_failed' >&2; exit 1; } ;; *) exit 64 ;; esac\n",
	}, 0o755)
	c.MustSh("mkdir -p /fake/log /fake/data/cadence/.install")
	boot := "/stock-bin:/bin"
	s := NewShell(box{c}, ShellOptions{Root: fakeRoot, DataRoot: "/data/cadence", BootPath: boot})
	ctx := context.Background()
	if out, err := s.RunScript(ctx, "/etc/init.d/S97x.sh", "start"); err != nil || out != "arg=start path="+boot+"\nerr\n" {
		t.Fatalf("start: %q %v", out, err)
	}
	c.MustSh("touch /fake/start-fails")
	if _, err := s.RunScript(ctx, "/etc/init.d/S97x.sh", "start"); err == nil || !strings.Contains(err.Error(), "status 7") {
		t.Fatalf("a failed start: %v", err)
	}
	c.MustSh("rm /fake/start-fails")
	if out, err := NewShell(box{c}, ShellOptions{Root: fakeRoot, DataRoot: "/data/cadence"}).RunScript(ctx, "/etc/init.d/S97x.sh", "stop"); err != nil ||
		!strings.HasPrefix(out, "arg=stop path=") || strings.Contains(out, "path="+boot) {
		t.Fatalf("without a boot PATH: %q %v", out, err)
	}
	c.MustSh(": > /fake/log/script")

	const wrapper, script = "/data/cadence/bin/cadence-run", "/etc/init.d/S97x.sh"
	if out, err := s.HandOver(ctx, wrapper, script); err != nil || out != "arg=start path="+boot+"\nerr\n" {
		t.Fatalf("hand-over: %q %v", out, err)
	}
	c.MustSh("touch /fake/release-fails")
	out, err := s.HandOver(ctx, wrapper, script)
	if err == nil || !strings.Contains(err.Error(), "watchdog_release_failed") || strings.Contains(err.Error(), "start failed") ||
		out != "arg=start path="+boot+"\nerr\n" {
		t.Fatalf("a failed release: %q %v", out, err)
	}
	c.MustSh("touch /fake/start-fails")
	if _, err := s.HandOver(ctx, wrapper, script); err == nil || !strings.Contains(err.Error(), "watchdog_release_failed") ||
		!strings.Contains(err.Error(), script+" start failed (status 7)") {
		t.Fatalf("both failed: %v", err)
	}
	if got := string(c.MustSh("cat /fake/log/release /fake/log/script")); got != strings.Repeat("release path="+boot+"\n", 3)+"start\nstart\nstart\n" {
		t.Fatalf("ran %q", got)
	}
}

// with a stage dir the scripts' output goes there (an overlay board's is a tmpfs, so a deleted capture a daemon holds
// costs RAM, not /data), made 700 when missing and each file removed once read; without one it goes to the work dir
func TestInitScriptCaptureGoesToStageDirOnBusybox(t *testing.T) {
	c := busyboxtest.Start(t)
	c.Tree(map[string]string{
		"/fake/etc/init.d/S97x.sh":           "readlink /proc/$$/fd/1\n",
		"/fake/data/cadence/bin/cadence-run": "#!/bin/sh\nexit 0\n",
	}, 0o755)
	c.MustSh("mkdir -p /fake/data/cadence/.install")
	ctx := context.Background()
	const wrapper, script = "/data/cadence/bin/cadence-run", "/etc/init.d/S97x.sh"
	staged := NewShell(box{c}, ShellOptions{Root: fakeRoot, DataRoot: "/data/cadence", StageDir: "/tmp/cadence-install"})
	if out, err := staged.RunScript(ctx, script, "start"); err != nil || out != "/fake/tmp/cadence-install/S97x.sh.start.out\n" {
		t.Fatalf("staged: %q %v", out, err)
	}
	if out, err := staged.HandOver(ctx, wrapper, script); err != nil || out != "/fake/tmp/cadence-install/S97x.sh.start.out\n" {
		t.Fatalf("staged hand-over: %q %v", out, err)
	}
	if got := string(c.MustSh("stat -c %a /fake/tmp/cadence-install && ls -A /fake/tmp/cadence-install /fake/data/cadence/.install")); got !=
		"700\n/fake/data/cadence/.install:\n\n/fake/tmp/cadence-install:\n" {
		t.Fatalf("the capture dirs after:\n%s", got)
	}
	work := NewShell(box{c}, ShellOptions{Root: fakeRoot, DataRoot: "/data/cadence"})
	if out, err := work.RunScript(ctx, script, "start"); err != nil || out != "/fake/data/cadence/.install/S97x.sh.start.out\n" {
		t.Fatalf("work dir: %q %v", out, err)
	}
}

// a daemon an init script leaves running keeps the script's stdout and stderr, as the stock's okcall and stereoctl
// do: those go to a file in the work dir, never the session's pipe, which has no reader once the session ends, so the
// daemon's first log line would be its SIGPIPE
func TestInitScriptDaemonsOutliveTheSessionOnBusybox(t *testing.T) {
	c := busyboxtest.Start(t)
	daemon := func(name string) string {
		return "echo " + name + "; sh -c 'sleep 2; echo x >&2; echo y; touch /fake/alive-" + name + "' &\n"
	}
	c.Tree(map[string]string{
		"/fake/etc/init.d/S97okcall.sh":      daemon("platform"),
		"/fake/etc/init.d/S97voice-agent.sh": daemon("stock"),
		"/fake/data/cadence/bin/cadence-run": "#!/bin/sh\necho released\n",
	}, 0o755)
	c.MustSh("mkdir -p /fake/data/cadence/.install")
	s := NewShell(box{c}, ShellOptions{Root: fakeRoot, DataRoot: "/data/cadence"})
	ctx := context.Background()
	if out, err := s.RunScript(ctx, "/etc/init.d/S97okcall.sh", "start"); err != nil || out != "platform\n" {
		t.Fatalf("platform: %q %v", out, err)
	}
	if out, err := s.HandOver(ctx, "/data/cadence/bin/cadence-run", "/etc/init.d/S97voice-agent.sh"); err != nil || out != "stock\n" {
		t.Fatalf("hand-over: %q %v", out, err)
	}
	if out, _, code := c.Sh(`for i in 1 2 3 4 5 6 7 8 9 10 11 12; do [ -e /fake/alive-platform ] && [ -e /fake/alive-stock ] && exit 0; sleep 0.5; done; ls /fake; exit 1`, nil); code != 0 {
		t.Fatalf("a daemon died with the session:\n%s", out)
	}
}
