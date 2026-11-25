package install

import (
	"context"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// swapInittab is the one command that makes the root writable: a script with its own exit trap, so the root goes
// back to read-only even if the session dies mid-write
//
//go:embed swap-inittab.sh
var swapInittab []byte

// Shell is a Device over a Runner: each method is one POSIX sh command for busybox (no bash), exit status 3 for a
// missing file, 4 for an existing destination, 5 for a process that is not the one named and 6 for a staged copy
// that arrived different.
type Shell struct {
	r    Runner
	opts ShellOptions
	// realData is the data root as the kernel resolves it (a midi's /data is a symlink to /bkupgrade/data), read once
	realData string
}

// ShellOptions are the device's paths and commands; tests swap the commands and put the device under a root.
type ShellOptions struct {
	// DataRoot bounds Remove; Work holds the inittab script, the installer's work dir when empty
	DataRoot, Work string
	// Inittab and Mounts are the device's own paths
	Inittab, Mounts string
	// Mount, Kill, Amixer and Pidof are commands, their own names when empty
	Mount, Kill, Amixer, Pidof string
	// Root prefixes every device path: "" on a device, a fake tree in tests
	Root string
	// StageDir is a second place Remove may delete under, where a board pushes its tarball
	StageDir string
	// HexRead reads files with dd and hexdump, for a busybox without head -c and od -t
	HexRead bool
	// BootPath is the PATH the init scripts and the wrapper's release run with, as init runs them, and the one every
	// other command searches before the session's; empty keeps the session's alone
	BootPath string
	// DryRun prints each change instead of making it; Out gets the lines
	DryRun bool
	Out    io.Writer
}

func NewShell(r Runner, o ShellOptions) *Shell {
	def := func(s *string, v string) {
		if *s == "" {
			*s = v
		}
	}
	def(&o.Mount, "mount")
	def(&o.Kill, "kill")
	def(&o.Amixer, "amixer")
	def(&o.Pidof, "pidof")
	def(&o.Mounts, "/proc/mounts")
	if o.DataRoot != "" {
		def(&o.Work, layoutOf(o.DataRoot).work)
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	return &Shell{r: r, opts: o}
}

// at is the device's path p as the commands name it
func (s *Shell) at(p string) string { return quote(s.opts.Root + p) }

// run runs one command; with a boot PATH the stock's applets come first and the session's PATH only after, as a
// session's own busybox may lack what the commands use (sha256sum -c) while the stock lacks others (awk)
func (s *Shell) run(ctx context.Context, script string) (Result, error) {
	if s.opts.BootPath != "" {
		script = "PATH=" + quote(s.opts.BootPath) + `:"$PATH"; export PATH; ` + script
	}
	return s.r.Run(ctx, script)
}

// ok runs a command that must exit 0
func (s *Shell) ok(ctx context.Context, what, script string) error {
	res, err := s.run(ctx, script)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s failed (status %d)%s", what, res.Code, tail(res.Out))
	}
	return nil
}

// change runs a command that changes the device, or prints it in a dry run
func (s *Shell) change(ctx context.Context, what, script string) error {
	if s.opts.DryRun {
		_, _ = fmt.Fprintf(s.opts.Out, "cadence-install: would run: %s\n", script)
		return nil
	}
	return s.ok(ctx, what, script)
}

// tail is the last line a failed command printed: busybox's own message, never a secret
func tail(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return ": " + last
	}
	return ""
}

func (s *Shell) Probe(ctx context.Context) error { return s.ok(ctx, "true", "true") }

func (s *Shell) Read(ctx context.Context, p string) ([]byte, bool, error) {
	script := fmt.Sprintf("[ -f %s ] || exit 3; head -c %d %s | od -An -v -tx1", s.at(p), maxRead+1, s.at(p))
	if s.opts.HexRead {
		script = hexReadScript(s.at(p), maxRead+1)
	}
	res, err := s.run(ctx, script)
	switch {
	case err != nil:
		return nil, false, err
	case res.Code == 3:
		return nil, false, nil
	case res.Code != 0:
		return nil, false, fmt.Errorf("reading %s failed (status %d)", p, res.Code)
	}
	data, err := hex.DecodeString(strings.Join(strings.Fields(string(res.Out)), ""))
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: unexpected output", p)
	}
	if len(data) > maxRead {
		return nil, false, fmt.Errorf("reading %s: larger than %d bytes", p, maxRead)
	}
	return data, true, nil
}

func (s *Shell) Exists(ctx context.Context, p string) (bool, error) {
	return s.test(ctx, "checking "+p, fmt.Sprintf("[ -e %s ] || [ -L %s ]", s.at(p), s.at(p)))
}

// test runs a check whose status is the answer: 0 yes, 1 no
func (s *Shell) test(ctx context.Context, what, script string) (bool, error) {
	res, err := s.run(ctx, script)
	switch {
	case err != nil:
		return false, err
	case res.Code == 0:
		return true, nil
	case res.Code == 1:
		return false, nil
	}
	return false, fmt.Errorf("%s failed (status %d)", what, res.Code)
}

func (s *Shell) List(ctx context.Context, dir string) ([]string, error) {
	// a glob, not ls: an old adbd runs commands on a pty, where busybox ls colours the names
	res, err := s.run(ctx, fmt.Sprintf(`[ -d %s ] || exit 3; cd %s || exit 2; `+
		`for f in .* *; do [ -e "$f" ] || [ -L "$f" ] && printf '%%s\n' "$f"; done; exit 0`, s.at(dir), s.at(dir)))
	switch {
	case err != nil:
		return nil, err
	case res.Code == 3:
		return nil, nil
	case res.Code != 0:
		return nil, fmt.Errorf("listing %s failed (status %d)", dir, res.Code)
	}
	var names []string
	for _, n := range strings.Split(string(res.Out), "\n") {
		if n != "" && n != "." && n != ".." {
			names = append(names, n)
		}
	}
	return names, nil
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Shell) Sum(ctx context.Context, p string) (string, bool, error) {
	res, err := s.run(ctx, fmt.Sprintf("[ -f %s ] || exit 3; sha256sum < %s", s.at(p), s.at(p)))
	switch {
	case err != nil:
		return "", false, err
	case res.Code == 3:
		return "", false, nil
	case res.Code != 0:
		return "", false, fmt.Errorf("hashing %s failed (status %d)", p, res.Code)
	}
	fields := strings.Fields(string(res.Out))
	if len(fields) == 0 || !sha256Hex.MatchString(fields[0]) {
		return "", false, fmt.Errorf("hashing %s: unexpected output", p)
	}
	return fields[0], true, nil
}

// Processes prints each process whose first or second argument is under the data root as a "proc <pid>" line, an
// "exe <path>" line when /proc names one, and an "arg <arg>" line per argument (a newline inside one becomes a
// space); every other command line is read on the device and dropped there
func (s *Shell) Processes(ctx context.Context) ([]Proc, error) {
	if s.opts.DataRoot == "" {
		return nil, errors.New("listing processes needs the data root")
	}
	script := fmt.Sprintf(`for d in %s/[0-9]*; do [ -r "$d/cmdline" ] || continue; a=$(tr '\000\n' '\n ' < "$d/cmdline" | awk -v r=%s 'NR <= 2 && index($0, r) == 1 { k = 1 } { print "arg " $0 } END { exit !k }') || continue; printf 'proc %%s\n' "${d##*/}"; e=$(readlink "$d/exe") && printf 'exe %%s\n' "$e"; printf '%%s\n' "$a"; done; true`,
		s.at("/proc"), quote(path.Clean(s.opts.DataRoot)+"/"))
	res, err := s.run(ctx, script)
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("listing processes failed (status %d)", res.Code)
	}
	procs := parseProcesses(res.Out)
	data, resolved, err := s.dataRoots(ctx)
	if err != nil {
		return nil, err
	}
	for i, p := range procs {
		if rest, ok := strings.CutPrefix(p.Exe, resolved+"/"); ok && resolved != data {
			procs[i].Exe = data + "/" + rest
		}
	}
	return procs, nil
}

// dataRoots is the data root as the installer names it and as the kernel resolves it. A process's exe is the
// resolved path, so one run from /data/cadence/cadenced reads as /bkupgrade/data/cadence/cadenced where /data is a
// symlink; Processes names it by the data root again, and Signal accepts either
func (s *Shell) dataRoots(ctx context.Context) (data, resolved string, err error) {
	if s.opts.DataRoot == "" {
		return "", "", nil
	}
	data = path.Clean(s.opts.DataRoot)
	if s.realData != "" {
		return data, s.realData, nil
	}
	res, err := s.run(ctx, "readlink -f "+s.at(data))
	if err != nil {
		return "", "", err
	}
	resolved = strings.TrimSpace(string(res.Out))
	if res.Code != 0 || !strings.HasPrefix(resolved, s.opts.Root+"/") || strings.Contains(resolved, "\n") {
		return "", "", fmt.Errorf("resolving %s failed (status %d)", data, res.Code)
	}
	s.realData = path.Clean(strings.TrimPrefix(resolved, s.opts.Root))
	return data, s.realData, nil
}

func parseProcesses(out []byte) []Proc {
	var procs []Proc
	for _, line := range strings.Split(string(out), "\n") {
		kind, value, _ := strings.Cut(line, " ")
		switch {
		case kind == "proc":
			if pid, err := strconv.Atoi(value); err == nil {
				procs = append(procs, Proc{Pid: pid})
			}
		case len(procs) == 0:
		case kind == "exe":
			procs[len(procs)-1].Exe = value
		case kind == "arg":
			procs[len(procs)-1].Args = append(procs[len(procs)-1].Args, value)
		}
	}
	return procs
}

func (s *Shell) Running(ctx context.Context, name string) (bool, error) {
	return s.test(ctx, "looking for "+name, quote(s.opts.Pidof)+" "+quote(name))
}

// RunningFrom prints a "proc <pid>" and an "exe <path>" line for each process whose executable lies under dir, as the
// kernel resolved it (" (deleted)" once replaced): nothing of a command line is read
func (s *Shell) RunningFrom(ctx context.Context, dir string) ([]Proc, error) {
	script := fmt.Sprintf(`for d in %s/[0-9]*; do e=$(readlink "$d/exe") || continue; case $e in %s*) printf 'proc %%s\nexe %%s\n' "${d##*/}" "$e" ;; esac; done; true`,
		s.at("/proc"), quote(path.Clean(dir)+"/"))
	res, err := s.run(ctx, script)
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("listing what runs from %s failed (status %d)", dir, res.Code)
	}
	return parseProcesses(res.Out), nil
}

func (s *Shell) Executable(ctx context.Context, p string) (bool, error) {
	return s.test(ctx, "checking "+p, fmt.Sprintf("[ -f %s ] && [ -x %s ]", s.at(p), s.at(p)))
}

func (s *Shell) FreeKB(ctx context.Context, p string) (int64, error) {
	// df's own status, not awk's: a path that is not there is an error, not an empty answer
	res, err := s.run(ctx, fmt.Sprintf(`o=$(df -Pk %s) || exit 2; printf '%%s\n' "$o" | awk 'NR == 2 { print $4 }'`, s.at(p)))
	if err != nil {
		return 0, err
	}
	kb, perr := strconv.ParseInt(strings.TrimSpace(string(res.Out)), 10, 64)
	if res.Code != 0 || perr != nil {
		return 0, fmt.Errorf("free space of %s unknown (status %d)", p, res.Code)
	}
	return kb, nil
}

func (s *Shell) Missing(ctx context.Context, tools []string) ([]string, error) {
	words := make([]string, len(tools))
	for i, t := range tools {
		words[i] = quote(t)
	}
	res, err := s.run(ctx, fmt.Sprintf(`for c in %s; do command -v "$c" || printf 'missing %%s\n' "$c"; done; true`, strings.Join(words, " ")))
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, line := range strings.Split(string(res.Out), "\n") {
		if t, ok := strings.CutPrefix(line, "missing "); ok {
			missing = append(missing, t)
		}
	}
	return missing, nil
}

func (s *Shell) LogCount(ctx context.Context, p, marker, needle string) (int, error) {
	res, err := s.run(ctx, fmt.Sprintf(`[ -f %s ] || exit 3; awk -v m=%s -v n=%s 'index($0, m) { c = 0 } index($0, n) { c++ } END { print c + 0 }' %s`,
		s.at(p), quote(marker), quote(needle), s.at(p)))
	switch {
	case err != nil:
		return 0, err
	case res.Code == 3:
		return 0, nil
	case res.Code != 0:
		return 0, fmt.Errorf("reading %s failed (status %d)", p, res.Code)
	}
	n, perr := strconv.Atoi(strings.TrimSpace(string(res.Out)))
	if perr != nil {
		return 0, fmt.Errorf("reading %s: unexpected output", p)
	}
	return n, nil
}

// channelLevel is a channel's line: "Front Left: Playback 115 [45%] [on]" for a playback volume, "Mono: 17 [17%]
// [-106.24dB]" for a volume amixer gives no direction (the vk capsula's TV-Set); a capture level never matches
var channelLevel = regexp.MustCompile(`^\s*[^:]+: (?:Playback )?-?\d+ \[(\d{1,3})%\]`)

func (s *Shell) Mixer(ctx context.Context, control string) ([]int, error) {
	res, err := s.run(ctx, quote(s.opts.Amixer)+" get "+quote(control))
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("amixer get failed (status %d)", res.Code)
	}
	return parseMixer(res.Out), nil
}

// parseMixer reads amixer's "<channel>: [Playback ]<raw> [<n>%] ..." lines
func parseMixer(out []byte) []int {
	var levels []int
	for _, line := range strings.Split(string(out), "\n") {
		if m := channelLevel.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			levels = append(levels, n)
		}
	}
	return levels
}

func (s *Shell) Level(ctx context.Context, wrapper string) (int, error) {
	res, err := s.run(ctx, "sh "+s.at(wrapper)+" level")
	if err != nil {
		return 0, err
	}
	n, perr := strconv.Atoi(strings.TrimSpace(string(res.Out)))
	if res.Code != 0 || perr != nil || n < 0 || n > 100 {
		return 0, fmt.Errorf("the wrapper's level is unknown (status %d)", res.Code)
	}
	return n, nil
}

func (s *Shell) Mkdir(ctx context.Context, p string, mode fs.FileMode) error {
	return s.change(ctx, "mkdir "+p, fmt.Sprintf("mkdir -p %s && chmod %o %s", s.at(p), mode, s.at(p)))
}

// staging is the temp name beside p that writes land on before their rename
func staging(p string) string { return path.Join(path.Dir(p), "."+path.Base(p)+".cadence-new") }

func (s *Shell) Write(ctx context.Context, p string, data []byte, mode fs.FileMode) error {
	if s.opts.DryRun {
		_, _ = fmt.Fprintf(s.opts.Out, "cadence-install: would write %s (%d bytes, mode %o)\n", p, len(data), mode)
		return nil
	}
	return s.write(ctx, p, data, mode)
}

func (s *Shell) WriteSecret(ctx context.Context, p string, data []byte) error {
	if s.opts.DryRun {
		_, _ = fmt.Fprintf(s.opts.Out, "cadence-install: would write %s (redacted, mode 600)\n", p)
		return nil
	}
	return s.write(ctx, p, data, 0o600)
}

// write stages data beside p and renames it over p
func (s *Shell) write(ctx context.Context, p string, data []byte, mode fs.FileMode) error {
	tmp, err := s.pushStaged(ctx, p, data)
	if err != nil {
		return err
	}
	return s.ok(ctx, "writing "+p, fmt.Sprintf("chmod %o %s && mv -f %s %s", mode, s.at(tmp), s.at(tmp), s.at(p)))
}

// pushStaged puts data in a local temp file of mode 600, pushes it to p's staging name and returns that name
func (s *Shell) pushStaged(ctx context.Context, p string, data []byte) (string, error) {
	f, err := os.CreateTemp("", "cadence-install-*")
	if err != nil {
		return "", err
	}
	local := f.Name()
	defer func() { _ = os.Remove(local) }()
	_, werr := f.Write(data)
	if cerr := f.Close(); werr != nil || cerr != nil {
		return "", errors.Join(werr, cerr)
	}
	tmp := staging(p)
	return tmp, s.r.Push(ctx, local, s.opts.Root+tmp)
}

func (s *Shell) Push(ctx context.Context, local, p string) error {
	if s.opts.DryRun {
		_, _ = fmt.Fprintf(s.opts.Out, "cadence-install: would push %s to %s\n", local, p)
		return nil
	}
	return s.r.Push(ctx, local, s.opts.Root+p)
}

func (s *Shell) Copy(ctx context.Context, from, to string, mode fs.FileMode) error {
	tmp := staging(to)
	return s.change(ctx, "copying "+from, fmt.Sprintf("cp %s %s && chmod %o %s && mv -f %s %s",
		s.at(from), s.at(tmp), mode, s.at(tmp), s.at(tmp), s.at(to)))
}

func (s *Shell) Rename(ctx context.Context, from, to string) error {
	return s.change(ctx, "moving "+from, fmt.Sprintf("if [ -e %s ] || [ -L %s ]; then exit 4; fi; mv %s %s",
		s.at(to), s.at(to), s.at(from), s.at(to)))
}

func (s *Shell) Remove(ctx context.Context, p string) error {
	if !s.removable(p) {
		return fmt.Errorf("refusing to remove %s outside the data root", p)
	}
	return s.change(ctx, "removing "+p, "rm -rf "+s.at(p))
}

// removable is a path under the data root, or under the stage dir a board pushes its tarball to
func (s *Shell) removable(p string) bool {
	p = path.Clean(p)
	for _, root := range []string{s.opts.DataRoot, s.opts.StageDir} {
		if root != "" && strings.HasPrefix(p, path.Clean(root)+"/") {
			return true
		}
	}
	return false
}

func (s *Shell) Unpack(ctx context.Context, tarball, dir string) error {
	return s.change(ctx, "unpacking "+tarball, fmt.Sprintf("mkdir %s && tar -xf %s -C %s", s.at(dir), s.at(tarball), s.at(dir)))
}

// Check drops MANIFEST's "#" header lines first: busybox sha256sum -c fails on a line it cannot read
func (s *Shell) Check(ctx context.Context, dir, manifest, sums string) error {
	return s.change(ctx, "checking "+dir+" against its manifest", fmt.Sprintf("grep -v '^#' %s > %s && cd %s && sha256sum -c -s %s",
		s.at(manifest), s.at(sums), s.at(dir), s.at(sums)))
}

func (s *Shell) Signal(ctx context.Context, p Proc, sig string) (bool, error) {
	args := make([]string, len(p.Args))
	for i, a := range p.Args {
		args[i] = quote(a)
	}
	proc := s.opts.Root + "/proc/" + strconv.Itoa(p.Pid)
	exe := p.Exe
	if data, resolved, err := s.dataRoots(ctx); err != nil {
		return false, err
	} else if rest, ok := strings.CutPrefix(p.Exe, data+"/"); ok && data != "" {
		exe = resolved + "/" + rest
	}
	script := fmt.Sprintf(`e=$(readlink %s); { [ "$e" = %s ] || [ "$e" = %s ]; } && [ "$(tr '\000' '\n' < %s)" = "$(printf '%%s\n' %s)" ] || exit 5; %s -%s %d`,
		quote(proc+"/exe"), quote(p.Exe), quote(exe), quote(proc+"/cmdline"), strings.Join(args, " "), quote(s.opts.Kill), sig, p.Pid)
	if s.opts.DryRun {
		_, _ = fmt.Fprintf(s.opts.Out, "cadence-install: would run: %s\n", script)
		return true, nil
	}
	res, err := s.run(ctx, script)
	switch {
	case err != nil:
		return false, err
	case res.Code == 0:
		return true, nil
	case res.Code == 5, res.Code == 1:
		// not the process named, or kill found it gone
		return false, nil
	}
	return false, fmt.Errorf("signalling %d failed (status %d)", p.Pid, res.Code)
}

func (s *Shell) SwapInittab(ctx context.Context, staged, oldSum, newSum string) error {
	script := path.Join(s.opts.Work, "swap-inittab.sh")
	if s.opts.DryRun {
		_, _ = fmt.Fprintf(s.opts.Out, "cadence-install: would replace %s with %s (sha256 %s), root writable only for the write\n",
			s.opts.Inittab, staged, newSum)
		return nil
	}
	if err := s.write(ctx, script, swapInittab, 0o700); err != nil {
		return err
	}
	return s.ok(ctx, "replacing "+s.opts.Inittab, strings.Join([]string{"sh", s.at(script), s.at(s.opts.Inittab), s.at(staged),
		quote(oldSum), quote(newSum), s.at(s.opts.Mounts), quote(s.opts.Mount), quote(s.opts.Kill), "2>&1"}, " "))
}

func (s *Shell) ReloadInit(ctx context.Context) error {
	return s.change(ctx, "reloading init", quote(s.opts.Kill)+" -HUP 1")
}

func (s *Shell) Blank(ctx context.Context, wrapper string) error {
	return s.change(ctx, "blanking the lights", "sh "+s.at(wrapper)+" blank")
}

// Prestart's failure carries the hook's own line (event tokens only)
func (s *Shell) Prestart(ctx context.Context, wrapper string) error {
	return s.change(ctx, "the board's prestart", "sh "+s.at(wrapper)+" prestart 2>&1")
}

// commLen is how much of a program's name the kernel keeps as the process's name
const commLen = 15

// StartScript runs start-stop-daemon -S -b -x /bin/sh -- script, whose child is detached from the session (adbd kills
// what a command leaves running). start-stop-daemon starts nothing when a process already runs as its -x program, and
// busybox counts a process whose argv[0] is /bin/sh, which adbd's session shell is, and init's scripts are: -n narrows
// that to one also named like the script, a copy its init script started, which counts as started (-o)
func (s *Shell) StartScript(ctx context.Context, script string) error {
	name := path.Base(script)
	if len(name) > commLen {
		name = name[:commLen]
	}
	return s.change(ctx, "starting "+script, fmt.Sprintf("start-stop-daemon -S -b -o -x /bin/sh -n %s -- %s 2>&1",
		quote(name), s.at(script)))
}

// StartDaemon runs start-stop-daemon -S -b -o -x exe -- args: matched by its executable, a daemon that runs already
// counts as started (-o)
func (s *Shell) StartDaemon(ctx context.Context, exe string, args []string) error {
	cmd := "start-stop-daemon -S -b -o -x " + s.at(exe)
	if len(args) > 0 {
		cmd += " --"
		for _, a := range args {
			cmd += " " + quote(a)
		}
	}
	return s.change(ctx, "starting "+exe, cmd+" 2>&1")
}

// Enroll reads the code from its file on the device, so it is in no command and no output
func (s *Shell) Enroll(ctx context.Context, e Enrollment) error {
	script := fmt.Sprintf(`%s enroll --server %s --code "$(cat %s)" --board %s --audiod --data-dir %s --ca-file %s --config %s`,
		s.at(e.Daemon), quote(e.Server), s.at(e.CodeFile), quote(e.Board), quote(e.DataDir), quote(e.CAFile), quote(e.Config))
	if s.opts.DryRun {
		_, _ = fmt.Fprintf(s.opts.Out, "cadence-install: would run: %s\n", script)
		return nil
	}
	res, err := s.run(ctx, script)
	switch {
	case err != nil:
		return err
	case res.Code == 3:
		return ErrEnrollRefused
	case res.Code != 0:
		return fmt.Errorf("cadenced enroll failed (status %d)%s", res.Code, tail(res.Out))
	}
	return nil
}
