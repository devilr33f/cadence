package install

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

// fake is an in-memory speaker for the phase tests: files, processes and a busybox init that starts its table's
// respawn entries when it reloads and restarts one whose process died while the entry is in the table it read last.
// ops records every change and signal in order; nothing else does.
type fake struct {
	t       *testing.T
	p       Profile
	l       layout
	files   map[string]*node
	procs   map[int]*fakeProc
	next    int
	loaded  []string
	rootRW  bool
	mixer   []int
	free    map[string]int64
	missing []string
	ops     []string
	// fail makes the change whose op starts with the key fail
	fail map[string]error
	// code is the code the fake server accepts; token the one it hands out
	code, token string
	// linkUp is whether a started cadenced logs link_up
	linkUp bool
	// pushed changes a pushed file on its way, as a broken transfer would
	pushed func(data []byte) []byte
	// reloaded runs after init read its table again, as a reload that goes wrong would
	reloaded func(*fake)
	// started runs after an overlay board's launcher started cadence-run, as a stock that comes back would
	started func(*fake)
	// stubborn and ghost name programs (by exe) that ignore TERM, and TERM and KILL
	stubborn, ghost map[string]bool
	// noLaunch is a stock start script that leaves no launcher running
	noLaunch bool
	// feeder is an overlay board's watchdog feeder: a subshell of cadence-run with its command line, which outlives
	// it through the hook's grace until a release or the next supervised prestart ends it
	feeder int
}

type node struct {
	data []byte
	mode fs.FileMode
	dir  bool
}

type fakeProc struct {
	Proc
	entry  string
	parent int
}

func newFake(t *testing.T, p Profile) *fake {
	f := &fake{
		t: t, p: p, l: layoutOf(p.DataRoot), files: map[string]*node{"/": {dir: true, mode: 0o755}},
		procs: map[int]*fakeProc{}, next: 100, free: map[string]int64{"/data": 1 << 20, "/": 8192},
		fail: map[string]error{}, code: "ABCD-EFGH", token: "cdd_secret-token", linkUp: true,
		stubborn: map[string]bool{}, ghost: map[string]bool{},
	}
	return f
}

// boot reads the inittab as init does at start
func (f *fake) boot() { f.reload() }

func (f *fake) record(format string, args ...any) error {
	op := fmt.Sprintf(format, args...)
	f.ops = append(f.ops, op)
	for k, err := range f.fail {
		if strings.HasPrefix(op, k) {
			return err
		}
	}
	return nil
}

func (f *fake) mkdirAll(p string) {
	for d := p; d != "/"; d = path.Dir(d) {
		if n, ok := f.files[d]; !ok {
			f.files[d] = &node{dir: true, mode: 0o755}
		} else if !n.dir {
			f.t.Fatalf("fake: %s is a file", d)
		}
	}
}

func (f *fake) put(p string, data []byte, mode fs.FileMode) {
	f.mkdirAll(path.Dir(p))
	f.files[p] = &node{data: bytes.Clone(data), mode: mode}
}

func (f *fake) get(p string) []byte {
	n, ok := f.files[p]
	if !ok || n.dir {
		return nil
	}
	return n.data
}

func (f *fake) has(p string) bool { _, ok := f.files[p]; return ok }

// under are p and every path below it
func (f *fake) under(p string) []string {
	var out []string
	for k := range f.files {
		if k == p || strings.HasPrefix(k, p+"/") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fake) spawn(proc Proc, entry string, parent int) int {
	f.next++
	f.procs[f.next] = &fakeProc{Proc: Proc{Pid: f.next, Exe: proc.Exe, Args: proc.Args}, entry: entry, parent: parent}
	return f.next
}

func (f *fake) running(entry string) bool {
	for _, p := range f.procs {
		if p.entry == entry {
			return true
		}
	}
	return false
}

func (f *fake) reload() {
	f.loaded = nil
	for _, l := range lines(f.get(f.p.Inittab)) {
		if e := strings.TrimSuffix(string(l), "\n"); strings.Contains(e, ":respawn:") {
			f.loaded = append(f.loaded, e)
		}
	}
	for _, e := range f.loaded {
		if !f.running(e) {
			f.start(e)
		}
	}
	if f.reloaded != nil {
		f.reloaded(f)
	}
}

// stockPids are the processes run from the stock's directory, and its launcher wherever it lies, by their executable
func (f *fake) stockPids() []int {
	if f.p.Stock == nil {
		return nil
	}
	var out []int
	for _, p := range f.procs {
		if strings.HasPrefix(p.Exe, f.p.Stock.Dir+"/") || p.Exe == f.p.Stock.Launcher {
			out = append(out, p.Pid)
		}
	}
	sort.Ints(out)
	return out
}

// startStock is the stock's start script with its launcher and an engine, as its init script runs them at boot
func (f *fake) startStock() {
	s := f.p.Stock
	sh := f.spawn(Proc{Exe: "/bin/busybox", Args: []string{"/bin/sh", s.Start}}, "", 0)
	if f.noLaunch {
		return
	}
	l := f.spawn(Proc{Exe: s.Launcher, Args: []string{s.Launcher}}, "", sh)
	f.spawn(Proc{Exe: s.Dir + "/maind", Args: []string{"maind"}}, "", l)
}

// suspendStock is the hook's suspend: every process run from the stock's directory is stopped but a ghost; false when
// one is left
func (f *fake) suspendStock() bool {
	for _, pid := range f.stockPids() {
		if p, ok := f.procs[pid]; ok && !f.ghost[p.Exe] {
			f.exit(pid)
		}
	}
	return len(f.stockPids()) == 0
}

// start runs an entry as the real speaker would: v1's supervisor and its cadenced, v2's wrapper (with its hook, the
// stock suspended where the board keeps one, the volume, then cadenced and audiod), or the program named
func (f *fake) start(entry string) {
	cmd := strings.Fields(entry[strings.LastIndex(entry, ":")+1:])
	switch entry {
	case f.l.v1Entry:
		f.suspendStock()
		sup := f.spawn(Proc{Exe: "/bin/busybox", Args: []string{"sh", f.l.v1Init}}, entry, 0)
		d := f.spawn(Proc{Exe: f.l.v1Daemon, Args: []string{f.l.v1Daemon, "--config", f.l.config}}, "", sup)
		f.put(f.l.v1Pidfiles[0], []byte(fmt.Sprintf("%d\n", sup)), 0o644)
		f.put(f.l.v1Pidfiles[1], []byte(fmt.Sprintf("%d\n", d)), 0o644)
	case f.l.v2Entry:
		f.runV2(entry)
	default:
		f.spawn(Proc{Exe: cmd[0], Args: cmd}, entry, 0)
	}
}

// runV2 is v2's wrapper, run by init from entry or by an overlay board's launcher with none, and returns its pid. on
// an overlay board its prestart also stops the platform's processes and takes the watchdog over from the last feeder
func (f *fake) runV2(entry string) int {
	w := f.spawn(Proc{Exe: "/bin/busybox", Args: []string{"/bin/sh", f.l.wrapper}}, entry, 0)
	// without its hook beside it the wrapper sets no level and starts nothing
	if !f.has(path.Join(path.Dir(f.l.wrapper), "board-hook")) {
		f.appendLog("cadence-run: 2026-11-09T12:00:00 event=board_control_failed code=hook")
		return w
	}
	if !f.suspendStock() {
		f.appendLog("cadence-run: 2026-11-09T12:00:00 event=board_control_failed code=suspend reason=stock_still_running")
		return w
	}
	if f.p.InitScript != "" {
		f.stopPlatform()
		f.exit(f.feeder)
		f.feeder = f.spawn(Proc{Exe: "/bin/busybox", Args: []string{"/bin/sh", f.l.wrapper}}, "", 0)
	}
	level, _ := f.Level(context.Background(), f.l.wrapper)
	f.mixer = []int{level, level}
	f.appendLog("cadence-run: 2026-11-09T12:00:00 event=start")
	if f.has(f.l.daemon) {
		d := f.spawn(Proc{Exe: f.l.daemon, Args: []string{f.l.daemon, "--config", f.l.config}}, "", w)
		f.spawn(Proc{Exe: f.l.audiod, Args: []string{f.l.audiod, "--socket", "/tmp/cadence/audiod.sock"}}, "", d)
		if f.linkUp {
			f.appendLog(`{"time":"2026-11-09T12:00:01Z","level":"INFO","msg":"link_up"}`)
		}
	}
	return w
}

// wrapperPids are the processes with v2's wrapper's command line: cadence-run, and on an overlay board its feeder
func (f *fake) wrapperPids() []int {
	var out []int
	for _, p := range f.procs {
		if slices.Equal(p.Args, []string{"/bin/sh", f.l.wrapper}) {
			out = append(out, p.Pid)
		}
	}
	sort.Ints(out)
	return out
}

// supervisor is the cadence-run the launcher's pid file names, 0 when the file is absent or stale
func (f *fake) supervisor() int {
	pid, err := strconv.Atoi(strings.TrimSpace(string(f.get(f.p.InitPidFile))))
	if p, ok := f.procs[pid]; err == nil && ok && slices.Equal(p.Args, []string{"/bin/sh", f.l.wrapper}) {
		return pid
	}
	return 0
}

// launcher is cadence's init script on an overlay board, as start-stop-daemon runs it with a pid file: start runs the
// wrapper detached unless the file names a live one, or the stock's copy when the tree is missing or disabled; stop
// TERMs the pid the file names, which ends its children but not the feeder, and leaves the file
func (f *fake) launcher(arg string) {
	switch arg {
	case "start":
		if !f.has(f.l.wrapper) || f.has(path.Join(f.l.root, "disabled")) {
			f.startStock()
			return
		}
		if f.supervisor() == 0 {
			f.put(f.p.InitPidFile, []byte(fmt.Sprintf("%d\n", f.runV2(""))), 0o644)
		}
		if f.started != nil {
			f.started(f)
		}
	case "stop":
		if pid := f.supervisor(); pid != 0 && !f.stubborn[f.procs[pid].Exe] && !f.ghost[f.procs[pid].Exe] {
			f.exit(pid)
		}
	}
}

// stopPlatform is an overlay board's prestart stopping the platform's processes
func (f *fake) stopPlatform() {
	for _, s := range f.p.PlatformScripts {
		for _, pid := range f.pidsOf("/usr/bin/" + s.Process) {
			f.exit(pid)
		}
	}
}

func (f *fake) appendLog(line string) {
	f.put(f.l.log, append(bytes.Clone(f.get(f.l.log)), line+"\n"...), 0o644)
}

// exit ends a process and its children; init restarts an entry it still has
func (f *fake) exit(pid int) {
	p, ok := f.procs[pid]
	if !ok {
		return
	}
	delete(f.procs, pid)
	for _, c := range f.procs {
		if c.parent == pid && !f.ghost[c.Exe] {
			f.exit(c.Pid)
		}
	}
	if p.entry != "" && slices.Contains(f.loaded, p.entry) {
		f.start(p.entry)
	}
}

func (f *fake) pidsOf(exe string) []int {
	var out []int
	for _, p := range f.procs {
		if p.Exe == exe {
			out = append(out, p.Pid)
		}
	}
	sort.Ints(out)
	return out
}

// reads

func (f *fake) Probe(context.Context) error { return nil }

func (f *fake) Read(_ context.Context, p string) ([]byte, bool, error) {
	n, ok := f.files[p]
	if !ok || n.dir {
		return nil, false, nil
	}
	if len(n.data) > maxRead {
		return nil, false, errors.New("too large")
	}
	return bytes.Clone(n.data), true, nil
}

func (f *fake) Exists(_ context.Context, p string) (bool, error) { return f.has(p), nil }

func (f *fake) List(_ context.Context, dir string) ([]string, error) {
	if n, ok := f.files[dir]; !ok || !n.dir {
		return nil, nil
	}
	var names []string
	for k := range f.files {
		if k != dir && path.Dir(k) == dir {
			names = append(names, path.Base(k))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (f *fake) Sum(_ context.Context, p string) (string, bool, error) {
	n, ok := f.files[p]
	if !ok || n.dir {
		return "", false, nil
	}
	return sum(n.data), true, nil
}

// Processes chooses as the device does: the data root in the first or second argument
func (f *fake) Processes(context.Context) ([]Proc, error) {
	var out []Proc
	for _, p := range f.procs {
		if !slices.ContainsFunc(p.Args[:min(len(p.Args), 2)], func(a string) bool { return strings.HasPrefix(a, f.p.DataRoot+"/") }) {
			continue
		}
		out = append(out, Proc{Pid: p.Pid, Exe: p.Exe, Args: slices.Clone(p.Args)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pid < out[j].Pid })
	return out, nil
}

func (f *fake) Running(_ context.Context, name string) (bool, error) {
	for _, p := range f.procs {
		if path.Base(p.Exe) == name {
			return true, nil
		}
	}
	return false, nil
}

func (f *fake) Executable(_ context.Context, p string) (bool, error) {
	n, ok := f.files[p]
	return ok && !n.dir && n.mode&0o111 != 0, nil
}

// RunningFrom is the device's exe scan: pid and executable only
func (f *fake) RunningFrom(_ context.Context, dir string) ([]Proc, error) {
	var out []Proc
	for _, p := range f.procs {
		if strings.HasPrefix(p.Exe, path.Clean(dir)+"/") {
			out = append(out, Proc{Pid: p.Pid, Exe: p.Exe})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pid < out[j].Pid })
	return out, nil
}

func (f *fake) FreeKB(_ context.Context, p string) (int64, error) { return f.free[p], nil }

// UsedKB counts each file under the paths in whole KiB, as du does its blocks; directories take none
func (f *fake) UsedKB(_ context.Context, paths ...string) (int64, error) {
	var kb int64
	for _, p := range paths {
		for _, k := range f.under(p) {
			if n := f.files[k]; !n.dir {
				kb += (int64(len(n.data)) + 1023) / 1024
			}
		}
	}
	return kb, nil
}

func (f *fake) Missing(context.Context, []string) ([]string, error) { return f.missing, nil }

func (f *fake) LogCount(_ context.Context, p, marker, needle string) (int, error) {
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(f.get(p)))
	for sc.Scan() {
		if strings.Contains(sc.Text(), marker) {
			n = 0
		}
		if strings.Contains(sc.Text(), needle) {
			n++
		}
	}
	return n, nil
}

func (f *fake) Mixer(context.Context, string) ([]int, error) { return slices.Clone(f.mixer), nil }

// Level is the wrapper's rule: cadenced's own file format, else 30
func (f *fake) Level(_ context.Context, wrapper string) (int, error) {
	if !f.has(wrapper) {
		return 0, errors.New("no wrapper")
	}
	if n, ok := volume.ParseLevel(f.get(f.l.volume)); ok {
		return n, nil
	}
	return 30, nil
}

// changes

func (f *fake) Mkdir(_ context.Context, p string, mode fs.FileMode) error {
	if err := f.record("mkdir %s %o", p, mode); err != nil {
		return err
	}
	f.mkdirAll(p)
	f.files[p].mode = mode
	return nil
}

func (f *fake) Write(_ context.Context, p string, data []byte, mode fs.FileMode) error {
	if err := f.record("write %s %o", p, mode); err != nil {
		return err
	}
	return f.write(p, data, mode)
}

func (f *fake) write(p string, data []byte, mode fs.FileMode) error {
	if n, ok := f.files[path.Dir(p)]; !ok || !n.dir {
		return fmt.Errorf("fake: no directory %s", path.Dir(p))
	}
	f.files[p] = &node{data: bytes.Clone(data), mode: mode}
	return nil
}

func (f *fake) WriteSecret(_ context.Context, p string, data []byte) error {
	if err := f.record("write-secret %s", p); err != nil {
		return err
	}
	return f.write(p, data, 0o600)
}

func (f *fake) Push(_ context.Context, local, p string) error {
	if err := f.record("push %s", p); err != nil {
		return err
	}
	data, err := os.ReadFile(local) //nolint:gosec // the test's own file
	if err != nil {
		return err
	}
	if f.pushed != nil {
		data = f.pushed(data)
	}
	return f.write(p, data, 0o644)
}

func (f *fake) Copy(_ context.Context, from, to string, mode fs.FileMode) error {
	if err := f.record("cp %s %s %o", from, to, mode); err != nil {
		return err
	}
	n, ok := f.files[from]
	if !ok || n.dir {
		return fmt.Errorf("fake: no file %s", from)
	}
	return f.write(to, n.data, mode)
}

func (f *fake) Rename(_ context.Context, from, to string) error {
	if err := f.record("mv %s %s", from, to); err != nil {
		return err
	}
	if !f.has(from) || f.has(to) {
		return fmt.Errorf("fake: cannot move %s to %s", from, to)
	}
	if n, ok := f.files[path.Dir(to)]; !ok || !n.dir {
		return fmt.Errorf("fake: no directory %s", path.Dir(to))
	}
	for _, k := range f.under(from) {
		f.files[to+strings.TrimPrefix(k, from)] = f.files[k]
		delete(f.files, k)
	}
	return nil
}

func (f *fake) Remove(_ context.Context, p string) error {
	if err := f.record("rm %s", p); err != nil {
		return err
	}
	if !strings.HasPrefix(p, f.p.DataRoot+"/") && (f.p.StageDir == "" || !strings.HasPrefix(p, f.p.StageDir+"/")) {
		f.t.Errorf("fake: removing %s, outside the data root", p)
		return errors.New("outside the data root")
	}
	for _, k := range f.under(p) {
		delete(f.files, k)
	}
	return nil
}

func (f *fake) Unpack(_ context.Context, tarball, dir string) error {
	if err := f.record("unpack %s %s", tarball, dir); err != nil {
		return err
	}
	if f.has(dir) {
		return fmt.Errorf("fake: %s exists", dir)
	}
	f.mkdirAll(dir)
	tr := tar.NewReader(bytes.NewReader(f.get(tarball)))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		p := path.Join(dir, h.Name)
		if h.Typeflag == tar.TypeDir {
			f.mkdirAll(p)
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		f.put(p, data, fs.FileMode(h.Mode))
	}
}

func (f *fake) Check(_ context.Context, dir, manifest, _ string) error {
	if err := f.record("check %s", dir); err != nil {
		return err
	}
	sums, _, err := parseManifest(f.get(manifest))
	if err != nil {
		return err
	}
	for name, want := range sums {
		if sum(f.get(path.Join(dir, name))) != want {
			return fmt.Errorf("fake: %s differs", name)
		}
	}
	return nil
}

func (f *fake) Signal(_ context.Context, p Proc, sig string) (bool, error) {
	if err := f.record("kill -%s %d", sig, p.Pid); err != nil {
		return false, err
	}
	fp, ok := f.procs[p.Pid]
	if !ok || fp.Exe != p.Exe || !slices.Equal(fp.Args, p.Args) {
		return false, nil
	}
	switch {
	case sig == "TERM" && !f.stubborn[fp.Exe] && !f.ghost[fp.Exe], sig == "KILL" && !f.ghost[fp.Exe]:
		f.exit(p.Pid)
	}
	return true, nil
}

// SwapInittab does what swap-inittab.sh does (tested on busybox): the root writable around the write, read-only
// again whatever happened, then init reloads
func (f *fake) SwapInittab(_ context.Context, staged, oldSum, newSum string) error {
	if err := f.record("swap-inittab %s", staged); err != nil {
		return err
	}
	if sum(f.get(f.p.Inittab)) != oldSum || sum(f.get(staged)) != newSum {
		return errors.New("fake: inittab changed or staged copy differs")
	}
	if !f.rootRW {
		f.ops = append(f.ops, "remount rw")
		f.rootRW = true
	}
	defer func() {
		f.ops = append(f.ops, "remount ro")
		f.rootRW = false
	}()
	if err := f.record("write-inittab"); err != nil {
		return err
	}
	f.files[f.p.Inittab].data = bytes.Clone(f.get(staged))
	f.ops = append(f.ops, "hup")
	f.reload()
	return nil
}

func (f *fake) ReloadInit(context.Context) error {
	if err := f.record("hup"); err != nil {
		return err
	}
	f.reload()
	return nil
}

func (f *fake) Blank(_ context.Context, wrapper string) error {
	if err := f.record("blank %s", wrapper); err != nil {
		return err
	}
	if !f.has(wrapper) {
		return errors.New("fake: no wrapper")
	}
	// the wrapper blanks through its board's hook, the file beside it
	if !f.has(path.Join(path.Dir(wrapper), "board-hook")) {
		return errors.New("fake: no board hook beside the wrapper")
	}
	return nil
}

// Prestart is the wrapper's prestart mode: its hook beside it, the stock suspended
func (f *fake) Prestart(_ context.Context, wrapper string) error {
	if err := f.record("prestart %s", wrapper); err != nil {
		return err
	}
	if !f.has(wrapper) || !f.has(path.Join(path.Dir(wrapper), "board-hook")) {
		return errors.New("fake: no wrapper with its hook")
	}
	// an overlay board's installer prestart stops the platform's processes and leaves the stock, the watchdog's
	// holder, to cadence-run's own
	if f.p.InitScript != "" {
		f.stopPlatform()
		return nil
	}
	if !f.suspendStock() {
		return errors.New("the board's prestart failed (status 1): cadence-run: event=board_control_failed code=suspend reason=stock_still_running")
	}
	return nil
}

func (f *fake) StartScript(_ context.Context, script string) error {
	if err := f.record("start-stop-daemon %s", script); err != nil {
		return err
	}
	if n, ok := f.files[script]; !ok || n.mode&0o111 == 0 {
		return fmt.Errorf("fake: %s is not executable", script)
	}
	f.startStock()
	return nil
}

func (f *fake) StartDaemon(_ context.Context, exe string, args []string) error {
	if err := f.record("start-stop-daemon -x %s %s", exe, strings.Join(args, " ")); err != nil {
		return err
	}
	f.spawn(Proc{Exe: exe, Args: append([]string{exe}, args...)}, "", 0)
	return nil
}

// WriteFile stands for the staged, sha256-checked rename; a broken transfer (pushed) is refused with path untouched
func (f *fake) WriteFile(_ context.Context, p string, data []byte, mode string) error {
	if err := f.record("write-file %s %s", p, mode); err != nil {
		return err
	}
	m, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return fmt.Errorf("fake: mode %q", mode)
	}
	if f.pushed != nil && !bytes.Equal(f.pushed(bytes.Clone(data)), data) {
		return fmt.Errorf("%w: fake: %s arrived different", ErrRefused, p)
	}
	return f.write(p, data, fs.FileMode(m))
}

// CopyFile stands for the staged, sha256-checked rename of a copy made on the device
func (f *fake) CopyFile(_ context.Context, src, dst, mode string) error {
	if err := f.record("copy-file %s %s %s", src, dst, mode); err != nil {
		return err
	}
	m, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return fmt.Errorf("fake: mode %q", mode)
	}
	n, ok := f.files[src]
	if !ok || n.dir {
		return fmt.Errorf("fake: no file %s", src)
	}
	return f.write(dst, n.data, fs.FileMode(m))
}

// RunScript runs InitScript as what it holds, cadence's launcher or the stock's copy, and a platform script's start
// starts its process (even when one runs, as the real ones do); any other script only records
func (f *fake) RunScript(_ context.Context, script, arg string) (string, error) {
	if err := f.record("run-script %s %s", script, arg); err != nil {
		return "", err
	}
	return "", f.runScript(script, arg)
}

func (f *fake) runScript(script, arg string) error {
	n, ok := f.files[script]
	if !ok || n.dir {
		return fmt.Errorf("fake: %s %s failed (status 2): can't open", script, arg)
	}
	for _, s := range f.p.PlatformScripts {
		if script == s.Script && arg == "start" {
			f.spawn(Proc{Exe: "/usr/bin/" + s.Process, Args: []string{s.Process}}, "", 0)
		}
	}
	switch {
	case script != f.p.InitScript:
	case bytes.Equal(n.data, f.p.InitLauncher):
		f.launcher(arg)
	case bytes.Equal(n.data, f.get(f.p.InitScriptLower)) && arg == "start":
		f.startStock()
	case bytes.Equal(n.data, f.get(f.p.InitScriptLower)) && arg == "stop":
		f.suspendStock()
	}
	return nil
}

// HandOver is the wrapper's release, its hook ending the feeder, then script's start, in one command: the start runs
// whether or not the release worked
func (f *fake) HandOver(_ context.Context, wrapper, script string) (string, error) {
	if err := f.record("hand-over %s %s", wrapper, script); err != nil {
		return "", err
	}
	var released error
	if !f.has(wrapper) || !f.has(path.Join(path.Dir(wrapper), "board-hook")) {
		released = errors.New("fake: no wrapper with its hook")
	} else {
		f.exit(f.feeder)
		f.feeder = 0
	}
	return "", errors.Join(released, f.runScript(script, "start"))
}

func (f *fake) Enroll(_ context.Context, e Enrollment) error {
	if err := f.record("enroll %s %s %s", e.Server, e.CodeFile, e.Config); err != nil {
		return err
	}
	if string(f.get(e.CodeFile)) != f.code {
		return ErrEnrollRefused
	}
	f.put(e.Config, []byte(`{"board":"`+e.Board+`","deviceToken":"`+f.token+`"}`), 0o600)
	return nil
}

// changes are the recorded ops that change the device or signal a process
func (f *fake) changes() []string { return slices.Clone(f.ops) }
