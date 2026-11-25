package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/install/busyboxtest"
)

// box runs the installer's commands in a busybox container, as ssh or adb would on the speaker
type box struct{ c *busyboxtest.Container }

func (b box) Run(_ context.Context, script string) (Result, error) {
	out, _, _ := b.c.Sh(wrap(script), nil)
	if res, ok := unwrap(out); ok {
		return res, nil
	}
	return Result{}, errors.New("no status")
}

func (b box) Push(_ context.Context, local, path string) error {
	data, err := os.ReadFile(local) //nolint:gosec // the test's own file
	if err != nil {
		return err
	}
	if out, _, _ := b.c.Sh(wrap("cat > "+quote(path)), data); !bytes.HasSuffix(out, []byte("cadence-rc=0\n")) {
		return fmt.Errorf("push %s: %s", path, out)
	}
	return nil
}

func (b box) String() string { return "busybox" }

// tap is a box that keeps everything the device printed back
type tap struct {
	box
	out bytes.Buffer
}

func (t *tap) Run(ctx context.Context, script string) (Result, error) {
	res, err := t.box.Run(ctx, script)
	t.out.Write(res.Out)
	return res, err
}

// fakeRoot is where the speaker's files live in the container; the commands with side effects are fakes that log
// their arguments under /fake/log
const fakeRoot = "/fake"

var fakeCommands = map[string]string{
	"/fake-bin/mount": `#!/bin/sh
echo "$*" >> /fake/log/mount
case "$*" in
	*remount,rw*) [ ! -e /fake/slow-rw ] || sleep 2 ;;
	*remount,ro*) [ ! -e /fake/fail-ro ] || exit 1 ;;
esac
`,
	"/fake-bin/kill":  "#!/bin/sh\necho \"$*\" >> /fake/log/kill\n",
	"/fake-bin/pidof": "#!/bin/sh\n[ -e \"/fake/running/$1\" ]\n",
	"/fake-bin/amixer": `#!/bin/sh
echo "$*" >> /fake/log/amixer
printf "Simple mixer control 'Master',0\n  Limits: Playback 0 - 255\n  Front Left: Playback 115 [45%%] [on]\n  Front Right: Playback 115 [45%%] [on]\n"
`,
	"/fake/data/cadence/bin/cadenced": `#!/bin/sh
printf '%s\n' "$@" > /fake/log/cadenced
exit "$(cat /fake/enroll-status 2>/dev/null || echo 0)"
`,
	"/fake/data/cadence/bin/cadence-run": "#!/bin/sh\ncase $1 in level) echo 45 ;; blank) echo blank >> /fake/log/blank ;; *) exit 64 ;; esac\n",
}

// v1Box is a container holding a v1 pickle under /fake, with v1's supervisor and cadenced in its /proc
func v1Box(t *testing.T) (*busyboxtest.Container, *Shell) {
	c := busyboxtest.Start(t)
	c.Tree(fakeCommands, 0o755)
	tree := map[string]string{}
	for p, s := range map[string]string{
		"/etc/inittab": original, "/proc/mounts": "rootfs / rootfs rw 0 0\n/dev/root / ext4 ro,relatime 0 0\n",
		"/proc/device-tree/model": "Test Board rev 3\x00", "/proc/sys/kernel/osrelease": "5.4.242\n",
		"/usr/lib/stock/VERSION": "3.21\n", "/data/cadence/cadenced.init": "#!/bin/sh\n", "/data/cadence/state/volume": "45\n",
		"/data/cadence/.hidden": "x", "/data/cadence/log/cadenced.log": "v1\n", "/data/cadence/stock/command/model": "m",
		"/proc/1/cmdline": "init\x00", "/proc/101/cmdline": "/usr/bin/adbd\x00",
		"/proc/102/cmdline": "sh\x00/data/cadence/cadenced.init\x00",
		"/proc/103/cmdline": "/data/cadence/cadenced\x00--config\x00/data/cadence/config.json\x00",
		"/proc/104/cmdline": "/usr/sbin/dropbear\x00-F\x00-p\x0022\x00-r\x00/etc/dropbear/x\x00",
		"/running/adbd":     "", "/running/dropbear": "", "/data/cadence/run/cadenced.pid": "103\n",
	} {
		tree[fakeRoot+p] = s
	}
	c.Tree(tree, 0o644)
	c.MustSh(`cd /fake/proc && ln -s /bin/busybox 1/exe && ln -s /usr/bin/adbd 101/exe && ln -s /bin/busybox 102/exe &&
		ln -s /data/cadence/cadenced 103/exe && ln -s /usr/sbin/dropbearmulti 104/exe && mkdir -p /fake/log /fake/proc/2 &&
		ln -s /nowhere /fake/data/cadence/dangling && chmod 755 /fake/data/cadence/cadenced.init`)
	return c, NewShell(box{c}, ShellOptions{
		DataRoot: "/data/cadence", Work: "/data/cadence/.install", Inittab: "/etc/inittab", Root: fakeRoot,
		Mount: "/fake-bin/mount", Kill: "/fake-bin/kill", Amixer: "/fake-bin/amixer", Pidof: "/fake-bin/pidof",
	})
}

// the device picks the processes the installer sees, cadence's by the data root in their first or second argument,
// so no other command line (soloist's key among them) crosses ssh or adb
func TestProcessesStayOnTheDevice(t *testing.T) {
	c, _ := v1Box(t)
	tree := map[string]string{}
	for p, s := range map[string]string{
		"/proc/105/cmdline": "/soloist/soloist\x00--device-name\x00Cadence\x00--api-key\x00not-for-the-transport\x00",
		"/proc/106/cmdline": "tail\x00-f\x00/data/cadence/log/cadenced.log\x00",
		"/proc/107/cmdline": "/data/cadence-old/cadenced\x00",
		"/proc/108/cmdline": "sh\x00/data/cadence/bin/cadence-run\x00",
	} {
		tree[fakeRoot+p] = s
	}
	c.Tree(tree, 0o644)
	r := &tap{box: box{c}}
	ctx := context.Background()
	procs, err := NewShell(r, ShellOptions{DataRoot: "/data/cadence", Root: fakeRoot}).Processes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, p := range procs {
		pids = append(pids, p.Pid)
	}
	slices.Sort(pids)
	if !slices.Equal(pids, []int{102, 103, 108}) {
		t.Fatalf("procs %+v", procs)
	}
	for _, other := range []string{"not-for-the-transport", "--api-key", "tail", "cadence-old", "adbd", "dropbear", "arg init"} {
		if strings.Contains(r.out.String(), other) {
			t.Errorf("%q crossed the transport:\n%s", other, r.out.String())
		}
	}
	if _, err := NewShell(r, ShellOptions{Root: fakeRoot}).Processes(ctx); err == nil {
		t.Fatal("processes listed without a data root to choose them by")
	}
}

func TestShellOnBusybox(t *testing.T) {
	c, s := v1Box(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Probe(ctx))

	if b, ok, err := s.Read(ctx, "/etc/inittab"); err != nil || !ok || string(b) != original {
		t.Fatalf("read %q %v %v", b, ok, err)
	}
	if b, _, _ := s.Read(ctx, "/proc/device-tree/model"); string(b) != "Test Board rev 3\x00" {
		t.Fatalf("model %q", b)
	}
	if _, ok, err := s.Read(ctx, "/nope"); ok || err != nil {
		t.Fatalf("missing: %v %v", ok, err)
	}
	c.MustSh("head -c 70000 /dev/zero > /fake/big")
	if _, _, err := s.Read(ctx, "/big"); err == nil {
		t.Fatal("read a file over the cap")
	}
	for p, want := range map[string]bool{"/etc/inittab": true, "/nope": false, "/data/cadence/dangling": true, "/data/cadence/log": true} {
		if got, err := s.Exists(ctx, p); err != nil || got != want {
			t.Errorf("exists %s = %v %v", p, got, err)
		}
	}
	if names, _ := s.List(ctx, "/data/cadence"); !slices.Equal(names, []string{".hidden", "bin", "cadenced.init", "dangling", "log", "run", "state", "stock"}) {
		t.Fatalf("list %q", names)
	}
	if got, ok, _ := s.Sum(ctx, "/etc/inittab"); !ok || got != sum([]byte(original)) {
		t.Fatalf("sum %s", got)
	}
	procs, err := s.Processes(ctx)
	must(err)
	want := []Proc{
		{Pid: 102, Exe: "/bin/busybox", Args: []string{"sh", "/data/cadence/cadenced.init"}},
		{Pid: 103, Exe: "/data/cadence/cadenced", Args: []string{"/data/cadence/cadenced", "--config", "/data/cadence/config.json"}},
	}
	slices.SortFunc(procs, func(a, b Proc) int { return a.Pid - b.Pid })
	slices.SortFunc(want, func(a, b Proc) int { return a.Pid - b.Pid })
	if fmt.Sprint(procs) != fmt.Sprint(want) {
		t.Fatalf("procs %+v", procs)
	}
	for name, want := range map[string]bool{"adbd": true, "maind": false} {
		if got, err := s.Running(ctx, name); err != nil || got != want {
			t.Errorf("running %s = %v %v", name, got, err)
		}
	}
	if kb, err := s.FreeKB(ctx, "/data"); err != nil || kb <= 0 {
		t.Fatalf("free %d %v", kb, err)
	}
	if m, err := s.Missing(ctx, []string{"tar", "sha256sum", "od", "awk", "cmp", "sed", "head", "df", "tr", "readlink", "no-such-tool"}); err != nil ||
		!slices.Equal(m, []string{"no-such-tool"}) {
		t.Fatalf("missing %q %v", m, err)
	}
	if n, err := s.LogCount(ctx, "/data/cadence/log/none.log", "event=start", "event=stock_resuspended"); err != nil || n != 0 {
		t.Fatalf("a missing log counted %d %v", n, err)
	}
	c.Put("/fake/data/cadence/log/cadenced.log", []byte("cadence-run: t event=start\n{\"msg\":\"link_up\"}\ncadence-run: t event=start\n"), "644")
	if n, _ := s.LogCount(ctx, "/data/cadence/log/cadenced.log", "event=start", `"msg":"link_up"`); n != 0 {
		t.Fatal("link_up from before the last start counted")
	}
	c.MustSh(`echo '{"time":"x","level":"INFO","msg":"link_up"}' >> /fake/data/cadence/log/cadenced.log && ` +
		`echo 'cadence-run: t event=stock_resuspended count=1' >> /fake/data/cadence/log/cadenced.log && ` +
		`echo 'cadence-run: t event=stock_resuspended count=2' >> /fake/data/cadence/log/cadenced.log`)
	if n, _ := s.LogCount(ctx, "/data/cadence/log/cadenced.log", "event=start", `"msg":"link_up"`); n != 1 {
		t.Fatalf("link_up after the start counted %d", n)
	}
	if n, _ := s.LogCount(ctx, "/data/cadence/log/cadenced.log", "event=start", "event=stock_resuspended"); n != 2 {
		t.Fatalf("re-suspends since the start counted %d", n)
	}
	if m, _ := s.Mixer(ctx, "Master"); !slices.Equal(m, []int{45, 45}) {
		t.Fatalf("mixer %v", m)
	}
	if l, err := s.Level(ctx, "/data/cadence/bin/cadence-run"); err != nil || l != 45 {
		t.Fatalf("level %d %v", l, err)
	}
	must(s.Blank(ctx, "/data/cadence/bin/cadence-run"))

	must(s.Mkdir(ctx, "/data/cadence/.install", 0o700))
	must(s.Write(ctx, "/data/cadence/state/volume", []byte(`{"level":45}`), 0o600))
	must(s.WriteSecret(ctx, "/data/cadence/.install/code", []byte("ABCD-EFGH")))
	must(s.Copy(ctx, "/etc/inittab", "/data/cadence/.install/inittab.copy", 0o640))
	must(s.Rename(ctx, "/data/cadence/.hidden", "/data/cadence/.install/hidden"))
	if err := s.Rename(ctx, "/data/cadence/log", "/data/cadence/stock"); err == nil {
		t.Fatal("moved onto an existing directory")
	}
	must(s.Remove(ctx, "/data/cadence/run"))
	if got := string(c.MustSh(`cd /fake/data/cadence && stat -c '%n %a' .install .install/code .install/inittab.copy state/volume && cat state/volume && ls -a .install && [ ! -e run ] && [ ! -e .hidden ]`)); got !=
		".install 700\n.install/code 600\n.install/inittab.copy 640\nstate/volume 600\n{\"level\":45}.\n..\ncode\nhidden\ninittab.copy\n" {
		t.Fatalf("after the changes:\n%s", got)
	}

	// signals only reach the process named, checked again on the device
	for _, tc := range []struct {
		p    Proc
		sent bool
	}{
		{Proc{Pid: 103, Exe: "/data/cadence/cadenced", Args: []string{"/data/cadence/cadenced", "--config", "/data/cadence/config.json"}}, true},
		{Proc{Pid: 103, Exe: "/usr/bin/other", Args: []string{"/data/cadence/cadenced", "--config", "/data/cadence/config.json"}}, false},
		{Proc{Pid: 103, Exe: "/data/cadence/cadenced", Args: []string{"/data/cadence/cadenced"}}, false},
		{Proc{Pid: 102, Exe: "/bin/busybox", Args: []string{"sh", "/data/cadence/cadenced.init"}}, true},
		{Proc{Pid: 999, Exe: "/data/cadence/cadenced", Args: []string{"/data/cadence/cadenced"}}, false},
	} {
		if sent, err := s.Signal(ctx, tc.p, "TERM"); err != nil || sent != tc.sent {
			t.Errorf("signal %+v = %v %v", tc.p, sent, err)
		}
	}
	must(s.ReloadInit(ctx))
	if got := string(c.MustSh("cat /fake/log/kill")); got != "-TERM 103\n-TERM 102\n-HUP 1\n" {
		t.Fatalf("kill got %q", got)
	}

	must(s.Enroll(ctx, Enrollment{Daemon: "/data/cadence/bin/cadenced", Server: "https://h", CodeFile: "/data/cadence/.install/code",
		Board: "yandex-pickle", DataDir: "/data/cadence", CAFile: "/data/cadence/share/ca-certificates.crt", Config: "/data/cadence/config.json"}))
	if got := string(c.MustSh("cat /fake/log/cadenced")); got != "enroll\n--server\nhttps://h\n--code\nABCD-EFGH\n--board\nyandex-pickle\n--audiod\n--data-dir\n/data/cadence\n--ca-file\n/data/cadence/share/ca-certificates.crt\n--config\n/data/cadence/config.json\n" {
		t.Fatalf("cadenced got:\n%s", got)
	}
	c.MustSh("echo 3 > /fake/enroll-status")
	if err := s.Enroll(ctx, Enrollment{Daemon: "/data/cadence/bin/cadenced", CodeFile: "/x"}); !errors.Is(err, ErrEnrollRefused) {
		t.Fatalf("err = %v", err)
	}
}

// ttyBox runs the installer's commands on a pseudo-terminal, as an old adbd (a midi's) runs `adb shell <command>`
type ttyBox struct{ c *busyboxtest.Container }

func (b ttyBox) Run(_ context.Context, script string) (Result, error) {
	if res, ok := unwrap(b.c.ShTTY(wrap(script))); ok {
		return res, nil
	}
	return Result{}, errors.New("no status")
}

func (b ttyBox) Push(context.Context, string, string) error { return errors.New("no push on a tty") }

func (b ttyBox) String() string { return "busybox tty" }

// regression: on a midi's adb pty busybox ls coloured the names, so stage moved paths like "\x1b[0;0mconfig.json\x1b[m"
func TestListOverATerminalOnBusybox(t *testing.T) {
	c := busyboxtest.Start(t)
	c.MustSh("mkdir -p /data/cadence/bin /data/cadence/log && touch /data/cadence/config.json /data/cadence/.hidden " +
		"&& chmod 755 /data/cadence/config.json && ln -s /nope /data/cadence/dangling && ln -s bin /data/cadence/link")
	s := shellOver(ttyBox{c}, false, nil)
	names, err := s.List(context.Background(), "/data/cadence")
	if err != nil || !slices.Equal(names, []string{".hidden", "bin", "config.json", "dangling", "link", "log"}) {
		t.Fatalf("list %q %v", names, err)
	}
	if names, err := s.List(context.Background(), "/data/cadence/log"); names != nil || err != nil {
		t.Fatalf("empty dir %q %v", names, err)
	}
}

// regression: a midi's /data is a symlink to /bkupgrade/data, so the kernel names v1's cadenced
// /bkupgrade/data/cadence/cadenced and stop-v1 left it running ("not a cadence process")
func TestProcessesThroughASymlinkedDataRootOnBusybox(t *testing.T) {
	c, s := v1Box(t)
	c.MustSh(`mkdir -p /fake/bkupgrade && mv /fake/data /fake/bkupgrade/data && ln -s /fake/bkupgrade/data /fake/data &&
		rm /fake/proc/103/exe && ln -s /bkupgrade/data/cadence/cadenced /fake/proc/103/exe`)
	ctx := context.Background()
	procs, err := s.Processes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(procs, func(p Proc) bool { return p.Pid == 103 })
	if i < 0 || !binary("cadenced", "/data/cadence/cadenced").match(procs[i]) {
		t.Fatalf("procs %+v", procs)
	}
	if sent, err := s.Signal(ctx, procs[i], "TERM"); err != nil || !sent {
		t.Fatalf("signal %v %v", sent, err)
	}
	if got := string(c.MustSh("cat /fake/log/kill")); got != "-TERM 103\n" {
		t.Fatalf("kill %q", got)
	}
}

// the tarball unpacks and checks with busybox's tar and sha256sum, gnu long names included; a changed file fails
func TestUnpackOnBusybox(t *testing.T) {
	c, s := v1Box(t)
	ctx := context.Background()
	long := "airplay/runtime/licenses/" + strings.Repeat("n", 120)
	files := append(bundleFiles(), bundleFile{name: long, data: "notice"})
	b, err := OpenBundle(writeBundle(t, files, bundleHeaders, nil), "yandex-pickle", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Mkdir(ctx, "/data/cadence/.install", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Push(ctx, b.Path, "/data/cadence/.install/cadence.tar"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Sum(ctx, "/data/cadence/.install/cadence.tar"); got != b.Sum {
		t.Fatalf("pushed sum %s, want %s", got, b.Sum)
	}
	if err := s.Unpack(ctx, "/data/cadence/.install/cadence.tar", "/data/cadence/.install/tree"); err != nil {
		t.Fatal(err)
	}
	check := func() error {
		return s.Check(ctx, "/data/cadence/.install/tree", "/data/cadence/.install/tree/MANIFEST", "/data/cadence/.install/sums")
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
	if got := string(c.MustSh("cat /fake/data/cadence/.install/tree/" + long + "; stat -c %a /fake/data/cadence/.install/tree/bin/cadenced")); got != "notice755\n" {
		t.Fatalf("unpacked %q", got)
	}
	c.MustSh("echo x >> /fake/data/cadence/.install/tree/bin/cadenced")
	if err := check(); err == nil {
		t.Fatal("a changed file passed the manifest")
	}
}

// swap-inittab.sh: the root writable only around the write and read-only after it, whatever ends the script
func TestSwapInittabOnBusybox(t *testing.T) {
	ctx := context.Background()
	next := []byte(stopped)
	for name, tc := range map[string]struct {
		setup      string
		oldSum     string
		newSum     string
		wantErr    string
		wantMount  string
		wantKill   string
		wantResult string
	}{
		"read-only root": {wantMount: "-o remount,rw /\n-o remount,ro /\n", wantKill: "-HUP 1\n", wantResult: stopped},
		"writable root": {setup: "printf '/dev/root / ext4 rw,relatime 0 0\\n' > /fake/proc/mounts",
			wantMount: "-o remount,ro /\n", wantKill: "-HUP 1\n", wantResult: stopped},
		"inittab changed": {oldSum: sum([]byte("other")), wantErr: "swap-inittab: inittab_changed", wantResult: original},
		"short copy":      {newSum: sum([]byte("other")), wantErr: "swap-inittab: staged_differs", wantResult: original},
		"write fails": {setup: "mkdir /fake/etc/inittab.cadence-new", wantErr: "replacing /etc/inittab failed (status 1)",
			wantMount: "-o remount,rw /\n-o remount,ro /\n", wantResult: original},
		"ro fails": {setup: "touch /fake/fail-ro", wantErr: "swap-inittab: root_still_writable", wantKill: "-HUP 1\n",
			wantMount: "-o remount,rw /\n" + strings.Repeat("-o remount,ro /\n", 5), wantResult: stopped},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, s := v1Box(t)
			// the inittab's own mode, which the staged copy (600) must not replace
			c.MustSh("chmod 640 /fake/etc/inittab")
			if tc.setup != "" {
				c.MustSh(tc.setup)
			}
			if err := s.Mkdir(ctx, "/data/cadence/.install", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := s.Write(ctx, "/data/cadence/.install/inittab.next", next, 0o600); err != nil {
				t.Fatal(err)
			}
			oldSum, newSum := sum([]byte(original)), sum(next)
			if tc.oldSum != "" {
				oldSum = tc.oldSum
			}
			if tc.newSum != "" {
				newSum = tc.newSum
			}
			// the script's own message reaches the owner, though ssh keeps stderr apart
			err := s.SwapInittab(ctx, "/data/cadence/.install/inittab.next", oldSum, newSum)
			if (err != nil) != (tc.wantErr != "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			got := string(c.MustSh("cat /fake/etc/inittab; stat -c %a /fake/etc/inittab; echo ==; cat /fake/log/mount 2>/dev/null; echo ==; cat /fake/log/kill 2>/dev/null; true"))
			if want := tc.wantResult + "640\n==\n" + tc.wantMount + "==\n" + tc.wantKill; got != want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// a session that dies mid-write still leaves the root read-only
func TestSwapInittabSurvivesASignal(t *testing.T) {
	c, s := v1Box(t)
	ctx := context.Background()
	if err := s.Mkdir(ctx, "/data/cadence/.install", 0o700); err != nil {
		t.Fatal(err)
	}
	// the script on the device, as SwapInittab pushes it
	if err := s.write(ctx, "/data/cadence/.install/swap-inittab.sh", swapInittab, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, "/data/cadence/.install/inittab.next", []byte(stopped), 0o600); err != nil {
		t.Fatal(err)
	}
	c.MustSh("touch /fake/slow-rw")
	out, _, code := c.Sh(fmt.Sprintf(`sh /fake/data/cadence/.install/swap-inittab.sh /fake/etc/inittab /fake/data/cadence/.install/inittab.next %s %s /fake/proc/mounts /fake-bin/mount /fake-bin/kill & pid=$!
sleep 1; kill -TERM $pid; wait $pid; echo "status $?"; cat /fake/log/mount; cat /fake/etc/inittab`, sum([]byte(original)), sum([]byte(stopped))), nil)
	if code != 0 || string(out) != "status 1\n-o remount,rw /\n-o remount,ro /\n"+original {
		t.Fatalf("status %d, got:\n%s", code, out)
	}
}

// a dry run against a v1 speaker reads it and changes nothing, the code never printed
func TestDryRunOnBusybox(t *testing.T) {
	c, s := v1Box(t)
	s.opts.DryRun = true
	var out bytes.Buffer
	s.opts.Out = &out
	snapshot := func() string {
		return string(c.MustSh("cd /fake && find . | sort | while read -r f; do if [ -f \"$f\" ]; then echo \"$f $(sha256sum < \"$f\")\"; else echo \"$f\"; fi; done"))
	}
	before := snapshot()
	o := Options{
		Bundle: testBundle(t), Server: "https://cadence.example", Code: "SECRET-CODE-1234", DryRun: true, Out: &out,
		Now: func() time.Time { return at }, Sleep: func(time.Duration) {},
	}
	p := testProfile()
	p.Tools = []string{"tar", "sha256sum"}
	if err := New(s, p, o).Install(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if after := snapshot(); after != before {
		t.Fatalf("a dry run changed the device")
	}
	printed := out.String()
	for _, want := range []string{
		"cadence-install: preflight: ok",
		"would replace /etc/inittab with /data/cadence/.install/inittab.next",
		"would run: e=$(readlink '/fake/proc/103/exe'); { [ \"$e\" = '/data/cadence/cadenced' ] || [ \"$e\" = '/data/cadence/cadenced' ]; }",
		"would run: if [ -e '/fake/data/cadence/v1/cadenced.init' ]",
		"would write /data/cadence/state/volume (12 bytes, mode 600)",
		"would push ",
		"would write /data/cadence/.install/code (redacted, mode 600)",
		"cadence-install: verify: would check",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("dry run lacks %q", want)
		}
	}
	if strings.Contains(printed, "SECRET-CODE-1234") {
		t.Fatal("a dry run printed the code")
	}
}

// a board that keeps its stock: the exe scan, the executable check, the wrapper's prestart and the stock's start, on
// busybox under /fake
func TestStockCommandsOnBusybox(t *testing.T) {
	c, s := v1Box(t)
	ctx := context.Background()
	c.Tree(map[string]string{
		"/fake/opt/stock/run.sh": "#!/bin/sh\necho \"$$ $0 $*\" >> /fake/log/stock\nsleep 31\n",
		"/fake/data/cadence/.install/cadence-run": `#!/bin/sh
case $1 in prestart) ;; *) exit 64 ;; esac
if [ -e /fake/stock-stuck ]; then echo 'cadence-run: event=board_control_failed code=suspend reason=stock_still_running' >&2; exit 1; fi
echo 'cadence-run: event=stock_suspended count=2' >&2
`,
	}, 0o755)
	c.Tree(map[string]string{
		"/fake/opt/stock/lib.so": "elf", "/fake/proc/201/cmdline": "launcher\x00", "/fake/proc/202/cmdline": "maind\x00",
		"/fake/proc/203/cmdline": "sh\x00/opt/stock/run.sh\x00--api-key\x00not-for-the-transport\x00",
		"/fake/proc/204/cmdline": "x\x00",
	}, 0o644)
	c.MustSh(`cd /fake/proc && ln -s /opt/stock/launcher 201/exe && ln -s '/opt/stock/maind (deleted)' 202/exe &&
		ln -s /bin/busybox 203/exe && ln -s /opt/stockade/x 204/exe && mkdir -p /fake/opt/stock/dir`)

	for p, want := range map[string]bool{"/opt/stock/run.sh": true, "/opt/stock/lib.so": false, "/opt/stock/dir": false, "/opt/stock/none": false} {
		if got, err := s.Executable(ctx, p); err != nil || got != want {
			t.Errorf("executable %s = %v %v", p, got, err)
		}
	}

	r := &tap{box: box{c}}
	procs, err := NewShell(r, ShellOptions{Root: fakeRoot}).RunningFrom(ctx, "/opt/stock")
	if err != nil {
		t.Fatal(err)
	}
	if want := []Proc{{Pid: 201, Exe: "/opt/stock/launcher"}, {Pid: 202, Exe: "/opt/stock/maind (deleted)"}}; fmt.Sprint(procs) != fmt.Sprint(want) {
		t.Fatalf("running from the stock: %+v", procs)
	}
	if strings.Contains(r.out.String(), "not-for-the-transport") || strings.Contains(r.out.String(), "arg") {
		t.Fatalf("a command line crossed the transport:\n%s", r.out.String())
	}

	if err := s.Prestart(ctx, "/data/cadence/.install/cadence-run"); err != nil {
		t.Fatal(err)
	}
	c.MustSh("touch /fake/stock-stuck")
	if err := s.Prestart(ctx, "/data/cadence/.install/cadence-run"); err == nil || !strings.Contains(err.Error(), "reason=stock_still_running") {
		t.Fatalf("err = %v", err)
	}

	// another shell runs as /bin/sh (adbd's session shell does): the start goes ahead anyway, detached from the session
	c.MustSh("/bin/sh -c 'sleep 32; :' > /dev/null 2>&1 &")
	if err := s.StartScript(ctx, "/opt/stock/run.sh"); err != nil {
		t.Fatal(err)
	}
	waitFor := func(want string) {
		t.Helper()
		for range 20 {
			if strings.Count(string(c.MustSh("cat /fake/log/stock 2>/dev/null; true")), "\n") == strings.Count(want, "\n") {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("the stock's log: %s", c.MustSh("cat /fake/log/stock 2>/dev/null; true"))
	}
	waitFor("1\n")
	// a copy its init script started (by its own name) still running counts as started
	c.MustSh("pkill -f '^sleep 31'; : > /fake/log/stock; /fake/opt/stock/run.sh > /dev/null 2>&1 &")
	waitFor("1\n")
	if err := s.StartScript(ctx, "/opt/stock/run.sh"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	waitFor("1\n")

	s.opts.DryRun = true
	var out bytes.Buffer
	s.opts.Out = &out
	if err := s.Prestart(ctx, "/data/cadence/.install/cadence-run"); err != nil {
		t.Fatal(err)
	}
	if err := s.StartScript(ctx, "/opt/stock/run.sh"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "would run: sh '/fake/data/cadence/.install/cadence-run' prestart") ||
		!strings.Contains(got, "would run: start-stop-daemon -S -b -o -x /bin/sh -n 'run.sh' -- '/fake/opt/stock/run.sh'") {
		t.Fatalf("dry run:\n%s", got)
	}
}
