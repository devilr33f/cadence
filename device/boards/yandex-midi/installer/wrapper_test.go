package installer_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/install/busyboxtest"
)

// the wrapper runs under busybox sh with its home at /t and midi's hook beside it: fb0 at /t/dev/fb0 with its sysfs at
// /t/fb, a fake /proc at /p, and fake amixer, cadenced, kill and sleep that log what they were given to /t/calls, in
// order. the kill also stamps each signal with its second in /t/stamps
const env = "CADENCE_HOME=/t CADENCE_AMIXER=/fake/amixer CADENCE_DEV=/t/dev CADENCE_FB_SYS=/t/fb CADENCE_PROC=/p " +
	"CADENCE_KILL=/fake/kill CADENCE_SLEEP=/fake/sleep"

// a fake process exits at any signal, unless /t/behave/<pid> says it ignores TERM (stubborn), ignores TERM and is gone
// only at the sleep after its KILL (slow), or never exits (unkillable). /t/on/<SIG>-<pid> runs as it is signalled
const fakeKill = `#!/bin/sh
sig=${1#-} pid=$2
echo "kill -$sig $pid" >> /t/calls
echo "$sig $pid $(date +%s)" >> /t/stamps
[ ! -f "/t/on/$sig-$pid" ] || sh "/t/on/$sig-$pid"
case $(cat "/t/behave/$pid" 2>/dev/null) in
	unkillable) ;;
	stubborn) [ "$sig" != KILL ] || rm -rf "/p/$pid" ;;
	slow) [ "$sig" != KILL ] || touch "/t/dying/$pid" ;;
	*) rm -rf "/p/$pid" ;;
esac
`

// returns at once; a slow process that was killed is gone after it
const fakeSleep = `#!/bin/sh
echo "sleep $*" >> /t/calls
for f in /t/dying/*; do
	[ ! -e "$f" ] || rm -rf "/p/${f##*/}" "$f"
done
`

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // the repo's own scripts
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func wrapperBox(t *testing.T) *busyboxtest.Container {
	t.Helper()
	c := busyboxtest.Start(t)
	c.Tree(map[string]string{
		"/t/bin/cadence-run": read(t, "../../../internal/install/files/cadence-run"),
		"/t/bin/board-hook":  read(t, "../files/board-hook"),
		"/t/bin/cadenced":    "#!/bin/sh\n. /t/cadenced.sh\n",
		"/fake/amixer":       "#!/bin/sh\necho \"amixer $*\" >> /t/calls\n",
		"/fake/kill":         fakeKill,
		"/fake/sleep":        fakeSleep,
	}, 0o755)
	c.Tree(map[string]string{
		"/t/fb/virtual_size": "89,1\n", "/t/fb/bits_per_pixel": "32\n", "/t/fb/stride": "356\n",
		"/t/cadenced.sh": "echo \"cadenced $*\" >> /t/calls\nexec sleep 30\n",
	}, 0o644)
	c.MustSh("mkdir -p /p /t/behave /t/on /t/dying /t/dev && head -c 400 /dev/urandom > /t/dev/fb0 && touch /t/calls")
	return c
}

// proc adds a fake process: the link to its executable as the kernel resolves it, its command line (a printf format)
// and what it does when signalled
func proc(c *busyboxtest.Container, pid int, exe, cmdline, behave string) {
	c.MustSh(fmt.Sprintf("mkdir -p /p/%[1]d && ln -s '%[2]s' /p/%[1]d/exe && printf '%[3]s' > /p/%[1]d/cmdline && echo '%[4]s' > /t/behave/%[1]d",
		pid, exe, cmdline, behave))
}

// supervise starts the wrapper, runs then (a script that waits) and stops it with TERM: its status, the calls and
// its log
func supervise(t *testing.T, c *busyboxtest.Container, extra, then string) (status int, calls, log string) {
	t.Helper()
	out, errOut, code := c.Sh(env+" "+extra+" sh /t/bin/cadence-run & pid=$!\n"+then+`
kill -TERM $pid; wait $pid; echo "status $?"; echo ==; cat /t/calls; echo ==; cat /t/log/cadenced.log`, nil)
	if code != 0 {
		t.Fatalf("status %d: %s", code, errOut)
	}
	parts := strings.SplitN(string(out), "==\n", 3)
	if len(parts) != 3 {
		t.Fatalf("output:\n%s", out)
	}
	if _, err := fmt.Sscanf(parts[0], "status %d", &status); err != nil {
		t.Fatalf("output:\n%s", out)
	}
	return status, parts[1], parts[2]
}

// inOrder fails unless text holds each of want, in this order
func inOrder(t *testing.T, text string, want ...string) {
	t.Helper()
	rest := text
	for _, w := range want {
		i := strings.Index(rest, w)
		if i < 0 {
			t.Fatalf("lacks %q in this order:\n%s", w, text)
		}
		rest = rest[i+len(w):]
	}
}

const blanked = "head -c 356 /dev/zero > /tmp/zero; if cmp -s /tmp/zero /t/dev/fb0; then echo blanked; else echo not; fi"

// before every start, the stock assistant is suspended by the executables the kernel resolved: its launcher first, so
// it restarts none of the rest, then every other vendor process; a process that merely names the vendor's directory
// on its command line is left alone, and one that left with the launcher is not signalled. stock that a watchdog
// brought back while cadenced ran is suspended again before the next start (M2m-D2)
func TestWrapperSuspendsTheStockBeforeEveryStart(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	proc(c, 100, "/system/vendor/quasar/maind", `maind\000`, "")
	proc(c, 150, "/bin/busybox", `sh\000/system/vendor/quasar/run.sh\000`, "")
	proc(c, 200, "/system/vendor/quasar/libs/helper (deleted)", `helper\000`, "")
	proc(c, 250, "/system/vendor/quasar/child", `/system/vendor/quasar/child\000`, "")
	proc(c, 300, "/system/vendor/quasar/quasar_launcher2", `quasar_launcher2\000`, "")
	c.Tree(map[string]string{
		"/t/on/TERM-300": "rm -rf /p/250\n",
		"/t/cadenced.sh": `echo "cadenced $*" >> /t/calls
if [ ! -e /t/ran ]; then
	touch /t/ran
	mkdir -p /p/400 && ln -s /system/vendor/quasar/quasar_launcher2 /p/400/exe
	exit 3
fi
exec sleep 30
`,
	}, 0o644)
	status, calls, log := supervise(t, c, "CADENCE_BACKOFF=1", "sleep 3")
	want := "kill -TERM 300\nkill -TERM 100\nkill -TERM 200\namixer -q sset Master 30%\ncadenced --config /t/config.json\n" +
		"kill -TERM 400\namixer -q sset Master 30%\ncadenced --config /t/config.json\n"
	if status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	inOrder(t, log, "event=stock_suspended count=4", "event=start", "event=exit rc=3", "event=stock_suspended count=1",
		"event=start", "event=stopped")
	if got := string(c.MustSh(blanked)); got != "blanked\n" {
		t.Errorf("fb0 is %s", got)
	}
}

// a process that ignores TERM is killed after 5 polls of a second, and one still there after its KILL gets a second
// more before the suspend counts it as failed
func TestWrapperKillsStockThatIgnoresTerm(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	proc(c, 100, "/system/vendor/quasar/maind", `maind\000`, "stubborn")
	proc(c, 200, "/system/vendor/quasar/quasar_launcher2", `quasar_launcher2\000`, "slow")
	status, calls, log := supervise(t, c, "", "sleep 2")
	want := "kill -TERM 200\nkill -TERM 100\n" + strings.Repeat("sleep 1\n", 5) + "kill -KILL 100\nkill -KILL 200\n" +
		"sleep 1\namixer -q sset Master 30%\ncadenced --config /t/config.json\n"
	if status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	inOrder(t, log, "event=stock_suspended count=2", "event=start")
}

// a vendor process that survives its KILL fails the suspend: the wrapper says so and tries again every 2 s, and starts
// cadenced (or sets the level) only once nothing of the vendor's runs. each attempt is timed by the stamps of its
// signals, the log only says what happened
func TestWrapperNeverStartsCadencedOverLiveStock(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	proc(c, 100, "/system/vendor/quasar/maind", `maind\000`, "unkillable")
	c.Tree(map[string]string{"/t/cadenced.sh": "echo \"cadenced at $(date +%s)\" >> /t/calls\nexec sleep 30\n"}, 0o644)
	status, calls, log := supervise(t, c, "", "sleep 5; date +%s > /t/freed; rm -rf /p/100; sleep 3")
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	var freed int
	if _, err := fmt.Sscan(string(c.MustSh("cat /t/freed")), &freed); err != nil {
		t.Fatal(err)
	}
	var terms []int
	for _, l := range strings.Split(strings.TrimSpace(string(c.MustSh("cat /t/stamps"))), "\n") {
		var sig string
		var pid, at int
		if _, err := fmt.Sscanf(l, "%s %d %d", &sig, &pid, &at); err != nil || pid != 100 {
			t.Fatalf("stamp %q", l)
		}
		if at > freed {
			t.Fatalf("signalled at %d, after the process was gone at %d", at, freed)
		}
		if sig == "TERM" {
			terms = append(terms, at)
		}
	}
	if len(terms) < 2 {
		t.Fatalf("attempts at %v", terms)
	}
	// a second of headroom: parallel tests load the host
	for i := 1; i < len(terms); i++ {
		if gap := terms[i] - terms[i-1]; gap < 2 || gap > 4 {
			t.Fatalf("attempts at %v: not every 2 s", terms)
		}
	}
	attempt := "kill -TERM 100\n" + strings.Repeat("sleep 1\n", 5) + "kill -KILL 100\nsleep 1\n"
	before, start, found := strings.Cut(calls, "amixer -q sset Master 30%\ncadenced at ")
	if !found || before != strings.Repeat(attempt, len(terms)) {
		t.Fatalf("calls\n%s", calls)
	}
	var started int
	if _, err := fmt.Sscan(start, &started); err != nil || started < freed || started > freed+3 {
		t.Fatalf("cadenced started at %q, the stock was gone at %d", start, freed)
	}
	if n := strings.Count(log, "event=board_control_failed code=suspend reason=stock_still_running"); n != len(terms) {
		t.Fatalf("%d failures logged for %d attempts:\n%s", n, len(terms), log)
	}
	inOrder(t, log, "reason=stock_still_running", "event=stock_suspended count=0", "event=start", "event=stopped")
	if strings.Count(log, "event=start") != 1 {
		t.Fatalf("log:\n%s", log)
	}
}

// a pid read again right before its signal: one that has become another program meanwhile, or is gone, is skipped
func TestWrapperSkipsAPidThatChangedBeforeItsSignal(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	proc(c, 100, "/system/vendor/quasar/maind", `maind\000`, "")
	proc(c, 200, "/system/vendor/quasar/helper", `helper\000`, "")
	proc(c, 250, "/system/vendor/quasar/child", `child\000`, "")
	c.Tree(map[string]string{"/t/on/TERM-100": "ln -sfn /bin/busybox /p/200/exe\nrm -rf /p/250\n"}, 0o644)
	status, calls, log := supervise(t, c, "", "sleep 2")
	if want := "kill -TERM 100\namixer -q sset Master 30%\ncadenced --config /t/config.json\n"; status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	inOrder(t, log, "event=stock_suspended count=3", "event=start")
}

// the ring is one frame of 89 pixels, 4 bytes each, on fb0: all 356 bytes are zeroed, and only on that geometry, as
// the board's sink checks it; any other is left alone and said so
func TestWrapperBlanksTheRing(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	if out, errOut, code := c.Sh(env+" sh /t/bin/cadence-run blank; "+blanked, nil); code != 0 || string(out) != "blanked\n" {
		t.Fatalf("status %d: %s%s", code, out, errOut)
	}
	// the ring's sysfs without its node: nothing to blank, and no failure, as on the pickle
	c.MustSh("rm /t/dev/fb0")
	if out, errOut, code := c.Sh(env+" sh /t/bin/cadence-run blank; echo \"status $?\"; [ ! -e /t/dev/fb0 ] && echo absent", nil); code != 0 ||
		string(out) != "status 0\nabsent\n" {
		t.Fatalf("no fb0: %s%s", out, errOut)
	}
	for name, change := range map[string]string{
		"width":  "echo 90,1 > /t/fb/virtual_size",
		"depth":  "echo 24 > /t/fb/bits_per_pixel",
		"stride": "echo 360 > /t/fb/stride",
		"sysfs":  "rm -rf /t/fb",
	} {
		c.MustSh("rm -rf /t/fb && mkdir /t/fb && echo 89,1 > /t/fb/virtual_size && echo 32 > /t/fb/bits_per_pixel && " +
			"echo 356 > /t/fb/stride && head -c 400 /dev/urandom > /t/dev/fb0 && " + change)
		before := c.MustSh("sha256sum < /t/dev/fb0")
		_, errOut, code := c.Sh(env+" sh /t/bin/cadence-run blank", nil)
		if after := c.MustSh("sha256sum < /t/dev/fb0"); code != 0 || string(after) != string(before) ||
			!strings.Contains(string(errOut), "event=blank_skipped") {
			t.Errorf("%s: status %d, fb0 changed %v: %s", name, code, string(after) != string(before), errOut)
		}
	}
}

// without its hook the wrapper cannot tell whether the stock still runs, so it starts nothing and sets no level, says
// so every 2 s, and stops on TERM; blank fails, and level still answers
func TestWrapperWithoutItsHookStartsNothing(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	c.MustSh("rm /t/bin/board-hook")
	proc(c, 100, "/system/vendor/quasar/maind", `maind\000`, "")
	status, calls, log := supervise(t, c, "", "sleep 3")
	if status != 0 || calls != "" {
		t.Fatalf("status %d, calls\n%s", status, calls)
	}
	if n := strings.Count(log, "event=board_control_failed code=hook"); n < 2 || strings.Contains(log, "event=start") ||
		!strings.Contains(log, "event=stopped") {
		t.Fatalf("log:\n%s", log)
	}
	if out, _, code := c.Sh(env+" sh /t/bin/cadence-run blank; "+blanked, nil); code != 0 || string(out) != "not\n" {
		t.Fatalf("blank without its hook: %s", out)
	}
	if out, _, code := c.Sh(env+" sh /t/bin/cadence-run blank", nil); code == 0 {
		t.Fatalf("blank without its hook succeeded: %s", out)
	}
	if out, _, _ := c.Sh(env+" sh /t/bin/cadence-run level", nil); string(out) != "30\n" {
		t.Fatalf("level: %q", out)
	}
}

// started waits in the wrapper's container until cadenced runs
const started = "until grep -q '^cadenced' /t/calls; do sleep 0.2; done\n"

// stock a late init script or a watchdog starts while cadenced runs is suspended at the wrapper's next poll, cadenced
// left running; a process that only names the vendor's directory on its command line is not (ruling M2m-R5)
func TestWrapperSuspendsStockThatStartsWhileCadencedRuns(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	status, calls, log := supervise(t, c, "CADENCE_POLL_SECONDS=1", started+`date +%s > /t/appeared
mkdir -p /p/500 && ln -s /system/vendor/quasar/quasar_launcher2 /p/500/exe
mkdir -p /p/510 && ln -s /bin/busybox /p/510/exe && printf 'sh\000/system/vendor/quasar/run.sh\000' > /p/510/cmdline
sleep 3`)
	want := "amixer -q sset Master 30%\ncadenced --config /t/config.json\nkill -TERM 500\namixer -q sset Master 30%\n"
	if status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	var appeared, pid, at int
	if _, err := fmt.Sscan(string(c.MustSh("cat /t/appeared")), &appeared); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscanf(string(c.MustSh("cat /t/stamps")), "TERM %d %d", &pid, &at); err != nil || pid != 500 || at > appeared+2 {
		t.Fatalf("signalled %d at %d (%v), the stock started at %d", pid, at, err, appeared)
	}
	inOrder(t, log, "event=stock_suspended count=0", "event=start", "event=stock_resuspended count=1", "event=stopped")
	if strings.Count(log, "event=start") != 1 || strings.Contains(log, "event=exit") {
		t.Fatalf("cadenced did not keep running:\n%s", log)
	}
}

// a late vendor process that will not stop is tried again at every poll while cadenced keeps running, and once it is
// gone the persisted level is set again, once, and nothing more is done
func TestWrapperRetriesStockThatWillNotStopWhileCadencedRuns(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	status, calls, log := supervise(t, c, "CADENCE_POLL_SECONDS=1", started+`mkdir -p /p/500 && ln -s /system/vendor/quasar/maind /p/500/exe
echo unkillable > /t/behave/500
sleep 3.5; rm -rf /p/500; sleep 2.5`)
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	attempt := "kill -TERM 500\n" + strings.Repeat("sleep 1\n", 5) + "kill -KILL 500\nsleep 1\n"
	rest, found := strings.CutPrefix(calls, "amixer -q sset Master 30%\ncadenced --config /t/config.json\n")
	n := strings.Count(rest, attempt)
	if !found || n < 2 || rest != strings.Repeat(attempt, n)+"amixer -q sset Master 30%\n" {
		t.Fatalf("calls\n%s", calls)
	}
	if got := strings.Count(log, "event=board_control_failed code=suspend reason=stock_still_running"); got != n {
		t.Fatalf("%d failures logged for %d attempts:\n%s", got, n, log)
	}
	if strings.Count(log, "event=start") != 1 || strings.Contains(log, "event=exit") || strings.Contains(log, "stock_resuspended") {
		t.Fatalf("log:\n%s", log)
	}
}

// stock that a running re-suspend stopped may have set its own volume before it went, so Master is set again to the
// level cadenced persisted last, read afresh; polls that find nothing set nothing (ruling M2m-R7)
func TestWrapperReappliesTheLevelAfterAResuspend(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	c.Tree(map[string]string{"/t/state/volume": `{"level":45}`}, 0o600)
	status, calls, log := supervise(t, c, "CADENCE_POLL_SECONDS=1", started+`sleep 2.5
printf '{"level":40}' > /t/state/volume
mkdir -p /p/500 && ln -s /system/vendor/quasar/quasar_launcher2 /p/500/exe
sleep 3.5`)
	want := "amixer -q sset Master 45%\ncadenced --config /t/config.json\nkill -TERM 500\namixer -q sset Master 40%\n"
	if status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	inOrder(t, log, "event=volume_applied level=45", "event=start", "event=stock_resuspended count=1",
		"event=volume_applied level=40", "event=stopped")
}

// a level cadenced persists while the wrapper sets the one it read is not overwritten: the wrapper reads the level
// again after its amixer and sets the new one, lower here, as the owner asked
func TestWrapperReappliesALevelChangedDuringItsReapply(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	c.Tree(map[string]string{"/t/state/volume": `{"level":45}`}, 0o600)
	// the second amixer is the re-apply: cadenced persists 20 while it runs
	c.Tree(map[string]string{"/fake/amixer": `#!/bin/sh
echo "amixer $*" >> /t/calls
echo x >> /t/amixer-runs
[ "$(wc -l < /t/amixer-runs)" -ne 2 ] || printf '{"level":20}' > /t/state/volume
`}, 0o755)
	status, calls, log := supervise(t, c, "CADENCE_POLL_SECONDS=1", started+`mkdir -p /p/500 && ln -s /system/vendor/quasar/quasar_launcher2 /p/500/exe
sleep 3`)
	want := "amixer -q sset Master 45%\ncadenced --config /t/config.json\nkill -TERM 500\namixer -q sset Master 45%\n" +
		"amixer -q sset Master 20%\n"
	if status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	inOrder(t, log, "event=stock_resuspended count=1", "event=volume_applied level=20", "event=stopped")
}

// an ls whose long listing shows no link targets is not taken to mean nothing of the vendor's runs: every link is
// read instead, before a start and while cadenced runs
func TestWrapperReadsEveryLinkWhenLsShowsNoTargets(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	c.Tree(map[string]string{"/fake/ls": "#!/bin/sh\nfor f in \"$@\"; do [ \"$f\" = -l ] || echo \"lrwxrwxrwx 1 root root 0 $f\"; done\n"}, 0o755)
	proc(c, 100, "/system/vendor/quasar/maind", `maind\000`, "")
	proc(c, 150, "/bin/busybox", `sh\000/system/vendor/quasar/run.sh\000`, "")
	status, calls, log := supervise(t, c, "CADENCE_LS=/fake/ls CADENCE_POLL_SECONDS=1", started+`mkdir -p /p/500 && ln -s /system/vendor/quasar/maind /p/500/exe
sleep 2.5`)
	want := "kill -TERM 100\namixer -q sset Master 30%\ncadenced --config /t/config.json\nkill -TERM 500\namixer -q sset Master 30%\n"
	if status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	inOrder(t, log, "event=stock_suspended count=1", "event=start", "event=stock_resuspended count=1", "event=stopped")
}

// cadence-run prestart is the suspend every start runs, once, for the installer before it stages anything: no level
// set, nothing started, and its outcome on stderr
func TestWrapperPrestartSuspendsOnce(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	proc(c, 100, "/system/vendor/quasar/maind", `maind\000`, "")
	proc(c, 200, "/system/vendor/quasar/quasar_launcher2", `quasar_launcher2\000`, "")
	out, errOut, code := c.Sh(env+" sh /t/bin/cadence-run prestart; echo \"status $?\"; cat /t/calls", nil)
	if code != 0 || string(out) != "status 0\nkill -TERM 200\nkill -TERM 100\n" ||
		!strings.Contains(string(errOut), "event=stock_suspended count=2") {
		t.Fatalf("status %d: %s%s", code, out, errOut)
	}
	c.MustSh(": > /t/calls")
	proc(c, 300, "/system/vendor/quasar/maind", `maind\000`, "unkillable")
	out, errOut, _ = c.Sh(env+" sh /t/bin/cadence-run prestart; echo \"status $?\"; cat /t/calls", nil)
	want := "status 1\nkill -TERM 300\n" + strings.Repeat("sleep 1\n", 5) + "kill -KILL 300\nsleep 1\n"
	if string(out) != want || !strings.Contains(string(errOut), "event=board_control_failed code=suspend reason=stock_still_running") {
		t.Fatalf("got\n%s%s\nwant\n%s", out, errOut, want)
	}
	if got := string(c.MustSh("[ -e /t/log ] && echo logged || echo none")); got != "none\n" {
		t.Fatalf("prestart wrote the log: %s", got)
	}
}

// a TERM while the stock will not stop ends the wrapper at once: no level set, nothing started
func TestWrapperStopsWhileTheStockWillNotStop(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	proc(c, 100, "/system/vendor/quasar/maind", `maind\000`, "unkillable")
	status, calls, log := supervise(t, c, "", "sleep 3")
	if status != 0 || strings.Contains(calls, "amixer") || strings.Contains(calls, "cadenced") {
		t.Fatalf("status %d, calls\n%s", status, calls)
	}
	inOrder(t, log, "reason=stock_still_running", "event=stopped")
	if strings.Contains(log, "event=start") {
		t.Fatalf("log:\n%s", log)
	}
}

// the platform's bluetooth player and agent are stopped by their executables before cadenced starts, and again at a
// poll once one came back while it runs; nothing else of the platform is signalled, and a poll that finds neither
// costs no sleep
func TestWrapperStopsTheBluetoothStock(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	proc(c, 600, "/usr/bin/bluealsa-aplay", `bluealsa-aplay\000-M\000disable\000-S\000`, "")
	proc(c, 610, "/usr/bin/just-works-bluez-agent", `just-works-bluez-agent\000`, "stubborn")
	proc(c, 620, "/usr/bin/bluealsa", `bluealsa\000`, "")
	status, calls, log := supervise(t, c, "CADENCE_POLL_SECONDS=1", started+`sleep 1.5
mkdir -p /p/700 && ln -s /usr/bin/bluealsa-aplay /p/700/exe
sleep 2.5`)
	want := "kill -TERM 600\nkill -TERM 610\nsleep 1\nkill -KILL 610\namixer -q sset Master 30%\ncadenced --config /t/config.json\n" +
		"kill -TERM 700\nsleep 1\n"
	if status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	inOrder(t, log, "event=stock_suspended count=0", "event=bluetooth_stock_stopped count=2", "event=start",
		"event=bluetooth_stock_stopped count=1", "event=stopped")
}
