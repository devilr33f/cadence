package installer_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/install/busyboxtest"
)

// the wrapper runs on the capsula's boot PATH, the stock busybox's applets alone (/stock-bin) with the fakes ahead of
// them, its home at /t and the hook beside it: a fake /proc at /p for the stock and the platform's daemons, the
// watchdog at /t/watchdog, the leds under /t/sys, the hook's runtime files in /t/run and the platform's init scripts in
// /fake/initd. the fake amixer, init scripts, kill and sleep log what they were given to /t/calls, in order; the kill
// also stamps each signal with its second in /t/stamps. the hook's own processes (its watchdog feeder) are real ones
const env = "PATH=/fake/bin:/stock-bin CADENCE_HOME=/t CADENCE_PROC=/p CADENCE_WATCHDOG=/t/watchdog CADENCE_SYS=/t/sys " +
	"CADENCE_INITD=/fake/initd CADENCE_KILL=/fake/kill CADENCE_SLEEP=/fake/sleep CADENCE_RUN_DIR=/t/run"

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

// the platform's init scripts the hook stops, in its order
var platformScripts = []string{"S97okcall.sh", "S96stereoctl.sh", "S99sysmon.sh", "S51ntp-ibb-volume-syncer"}

// a platform init script logs its argument and runs /t/on/<name>, if a test left one
func initScript(name string) string {
	return "#!/bin/sh\necho \"" + name + " $1\" >> /t/calls\n[ ! -f /t/on/" + name + " ] || sh /t/on/" + name + "\n"
}

// what the platform's init scripts log when the hook stops them
var platformStops = func() string {
	var b strings.Builder
	for _, s := range platformScripts {
		b.WriteString(s + " stop\n")
	}
	return b.String()
}()

func hookBox(t *testing.T) *busyboxtest.Container {
	t.Helper()
	c := busyboxtest.Start(t)
	c.StockBin("/stock-bin", busyboxtest.CapsulaBootApplets)
	files := map[string]string{
		"/t/bin/cadence-run": read(t, "../../../internal/install/files/cadence-run"),
		"/t/bin/board-hook":  read(t, "../files/board-hook"),
		"/t/bin/cadenced":    "#!/bin/sh\n. /t/cadenced.sh\n",
		"/fake/bin/amixer":   "#!/bin/sh\necho \"amixer $*\" >> /t/calls\n",
		"/fake/kill":         fakeKill,
		"/fake/sleep":        fakeSleep,
	}
	for _, s := range platformScripts {
		files["/fake/initd/"+s] = initScript(s)
	}
	c.Tree(files, 0o755)
	c.Tree(map[string]string{"/t/cadenced.sh": "echo \"cadenced $*\" >> /t/calls\nexec sleep 30\n"}, 0o644)
	c.MustSh(`mkdir -p /p /t/behave /t/on /t/dying && touch /t/calls /t/watchdog /tmp/okcall /tmp/tgclient &&
for i in $(seq 36); do mkdir -p /t/sys/class/leds/top-led$i && echo 255 > /t/sys/class/leds/top-led$i/brightness; done &&
for i in $(seq 30); do mkdir -p /t/sys/class/leds/clock-led$i && echo 153 > /t/sys/class/leds/clock-led$i/brightness; done`)
	return c
}

// proc adds a fake process: its comm (as the kernel cuts it to 15 bytes), its command line (a printf format) and what
// it does when signalled
func proc(c *busyboxtest.Container, pid int, comm, cmdline, behave string) {
	c.MustSh(fmt.Sprintf("mkdir -p /p/%[1]d && echo '%[2]s' > /p/%[1]d/comm && printf '%[3]s' > /p/%[1]d/cmdline && echo '%[4]s' > /t/behave/%[1]d",
		pid, comm, cmdline, behave))
}

// holds gives a fake process the watchdog on its fd 3
func holds(c *busyboxtest.Container, pid int) {
	c.MustSh(fmt.Sprintf("mkdir -p /p/%[1]d/fd && ln -s /t/watchdog /p/%[1]d/fd/3", pid))
}

// stock is the stock assistant as it runs: voice-agent's master (300), its app (100), which opened the watchdog, and
// the app's btctl (120) and tgclient (130), which inherited it; the master's pid is the highest, so it is first only by
// the hook's choice
func stock(c *busyboxtest.Container, app string) {
	proc(c, 300, "voice-agent", `voice-agent\000-mode=master\000`, "")
	proc(c, 100, "voice-agent", `voice-agent\000-mode=app\000-watchdog\000`, app)
	proc(c, 120, "btctl", `btctl\000-logtosyslog\000`, "")
	proc(c, 130, "tgclient", `/tmp/tgclient\000-ipc-fd48\000`, "")
	for _, pid := range []int{100, 120, 130} {
		holds(c, pid)
	}
}

// stampFeeds makes the watchdog a fifo with a reader that holds it open (so no open of it blocks) and appends each
// byte fed to /t/fed and the time since boot it came at to /t/feeds
func stampFeeds(c *busyboxtest.Container) {
	c.MustSh(`rm -f /t/watchdog && mkfifo /t/watchdog && touch /t/fed /t/feeds
nohup sh -c 'exec 4<>/t/watchdog; while :; do dd bs=1 count=1 <&4 >> /t/fed 2>/dev/null; read -r u r < /proc/uptime; echo "$u" >> /t/feeds; done' < /dev/null > /dev/null 2>&1 &`)
}

// feeds are the times of the bytes fed so far, and the bytes
func feeds(t *testing.T, c *busyboxtest.Container) ([]float64, string) {
	t.Helper()
	out := string(c.MustSh("cat /t/feeds; echo ==; cat /t/fed"))
	stamps, fed, _ := strings.Cut(out, "==\n")
	var times []float64
	for _, l := range strings.Fields(stamps) {
		v, err := strconv.ParseFloat(l, 64)
		if err != nil {
			t.Fatalf("feed stamp %q", l)
		}
		times = append(times, v)
	}
	return times, fed
}

// stamp is the time since boot a test script wrote to a file
func stamp(t *testing.T, c *busyboxtest.Container, path string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(strings.TrimSpace(string(c.MustSh("cat "+path))), 64)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// stampAt writes the time since boot to path, in a test script
func stampAt(path string) string {
	return "read -r u r < /proc/uptime; echo \"$u\" > " + path + "\n"
}

// maxGap is the longest time between two feeds in a row
func maxGap(times []float64) float64 {
	var gap float64
	for i := 1; i < len(times); i++ {
		gap = max(gap, times[i]-times[i-1])
	}
	return gap
}

// after counts the feeds later than at
func after(times []float64, at float64) int {
	n := 0
	for _, v := range times {
		if v > at {
			n++
		}
	}
	return n
}

// waitFor waits in a test script for a shell condition, at most 10 s
func waitFor(cond string) string {
	return "i=0; until " + cond + "; do i=$((i+1)); [ $i -lt 100 ] || break; sleep 0.1; done\n"
}

// started waits until cadenced runs
var started = waitFor("grep -q '^cadenced' /t/calls")

// feederOf is the pid the hook's pid file names
func feederOf(t *testing.T, c *busyboxtest.Container, path string) int {
	t.Helper()
	var pid, owner int
	if _, err := fmt.Sscan(string(c.MustSh("cat "+path)), &pid, &owner); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return pid
}

// holding is whether a real process holds the watchdog on its fd 3
func holding(c *busyboxtest.Container, pid int) bool {
	return string(c.MustSh(fmt.Sprintf("[ /proc/%d/fd/3 -ef /t/watchdog ] && echo yes || echo no", pid))) == "yes\n"
}

// launch starts the supervising wrapper in the background of a test script, as the launcher does: its stdio off the
// script's (what is left of it after a stop holds no pipe of docker exec's), its stderr kept in /t/stderr
func launch(extra, pid string) string {
	return env + " " + extra + " sh /t/bin/cadence-run < /dev/null > /dev/null 2>> /t/stderr & " + pid + "=$!\n"
}

// quiet fails a test whose wrapper wrote anything to stderr, a shell error in the hook say, but busybox ash's own
// Terminated: it prints that when a wait reaps a job a TERM ended, as cadence-run's stop's wait for cadenced may
func quiet(t *testing.T, c *busyboxtest.Container) {
	t.Helper()
	for _, l := range strings.Split(string(c.MustSh("cat /t/stderr 2>/dev/null; true")), "\n") {
		if l != "" && l != "Terminated" {
			t.Fatalf("the wrapper's stderr: %q", l)
		}
	}
}

// supervise starts the wrapper, runs then (a script that waits) and stops it with TERM: its status, the calls and
// its log
func supervise(t *testing.T, c *busyboxtest.Container, extra, then string) (status int, calls, log string) {
	t.Helper()
	out, errOut, code := c.Sh(launch(extra, "pid")+then+`
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
	quiet(t, c)
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

// only keeps the lines of text that start with one of prefixes
func only(text string, prefixes ...string) string {
	var b strings.Builder
	for _, l := range strings.SplitAfter(text, "\n") {
		for _, p := range prefixes {
			if strings.HasPrefix(l, p) {
				b.WriteString(l)
				break
			}
		}
	}
	return b.String()
}

// before the first start the platform's daemons are stopped by their init scripts and any left by its comm, then the
// stock assistant: voice-agent's master first, then its app, btctl and tgclient. the platform's pulseaudio and
// capsula-root's dropbear are never signalled, the stock's unpacked copies in /tmp are freed, and TV-Set holds the
// level before cadenced starts
func TestHookStopsTheStockInOrder(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stock(c, "")
	proc(c, 140, "pulseaudio", `/usr/bin/pulseaudio\000--system\000`, "")
	proc(c, 150, "dropbearmulti", `dropbear\000-R\000`, "")
	proc(c, 160, "sysmon", `sysmon\000`, "")
	proc(c, 170, "ntp-ibb-volume-", `ntp-ibb-volume-syncer\000`, "")
	c.Tree(map[string]string{"/t/on/S51ntp-ibb-volume-syncer": "rm -rf /p/170\n"}, 0o644)
	status, calls, log := supervise(t, c, "", started+"sleep 0.5")
	want := platformStops + "kill -TERM 160\nkill -TERM 300\nkill -TERM 100\nkill -TERM 120\nkill -TERM 130\n" +
		"amixer -q sset Master 255\namixer -q sset TV-Set 30\ncadenced --config /t/config.json\n"
	if status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	inOrder(t, log, "event=platform_stopped count=2", "event=stock_suspended count=4", "event=watchdog_opened pid=",
		"event=volume_applied level=30", "event=start", "event=stopped")
	if got := string(c.MustSh("ls /p; ls /tmp")); got != "140\n150\n" {
		t.Fatalf("left in /p and /tmp:\n%s", got)
	}
}

// a stock process that ignores TERM gets KILL after 3 s (15 polls of 0.2 s), and one still there after its KILL is
// waited for before the watchdog is taken
func TestHookKillsStockThatIgnoresTerm(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stock(c, "stubborn")
	c.MustSh("echo slow > /t/behave/120")
	status, calls, log := supervise(t, c, "", started+"sleep 0.5")
	want := platformStops + "kill -TERM 300\nkill -TERM 100\nkill -TERM 120\nkill -TERM 130\n" + strings.Repeat("sleep 0.2\n", 15) +
		"kill -KILL 100\nkill -KILL 120\nsleep 0.2\namixer -q sset Master 255\namixer -q sset TV-Set 30\ncadenced --config /t/config.json\n"
	if status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	inOrder(t, log, "event=stock_suspended count=4", "event=watchdog_opened", "event=start")
}

// a stock process that survives its KILL fails the prestart: nothing takes the watchdog, which it still holds, and
// cadenced does not start; the wrapper tries again 2 s later, and the app, gone at its second KILL, lets it start
func TestHookNeverTakesTheWatchdogFromStockThatWillNotStop(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stock(c, "unkillable")
	stampFeeds(c)
	c.Tree(map[string]string{"/t/on/KILL-100": "if [ -e /t/killed ]; then : > /t/behave/100\n" + stampAt("/t/freed") +
		"fi\ntouch /t/killed\n"}, 0o644)
	status, calls, log := supervise(t, c, "", started+"sleep 0.5")
	polls := strings.Repeat("sleep 0.2\n", 15)
	want := platformStops + "kill -TERM 300\nkill -TERM 100\nkill -TERM 120\nkill -TERM 130\n" + polls + "kill -KILL 100\n" +
		strings.Repeat("sleep 0.2\n", 10) + platformStops + "kill -TERM 100\n" + polls + "kill -KILL 100\n" +
		"amixer -q sset Master 255\namixer -q sset TV-Set 30\ncadenced --config /t/config.json\n"
	if status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	inOrder(t, log, "event=board_control_failed code=stock reason=still_running", "event=stock_suspended count=1",
		"event=watchdog_opened", "event=start")
	if strings.Count(log, "board_control_failed") != 1 || strings.Count(log, "event=watchdog_opened") != 1 {
		t.Fatalf("log:\n%s", log)
	}
	times, _ := feeds(t, c)
	if freed := stamp(t, c, "/t/freed"); len(times) == 0 || times[0] < freed {
		t.Fatalf("fed at %v, the stock let the watchdog go at %.2f", times, freed)
	}
}

// the watchdog is single-open: while any process holds it, as its fds in /proc show, the feeder does not open it, and
// it does within 0.2 s of the last holder letting go
func TestHookTakesTheWatchdogAfterTheHoldersExit(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	proc(c, 400, "watchdogd", `watchdogd\000`, "")
	holds(c, 400)
	stampFeeds(c)
	status, calls, log := supervise(t, c, "", "sleep 1.5\n"+stampAt("/t/freed")+"rm -rf /p/400\n"+started+"sleep 0.5")
	if want := platformStops + "amixer -q sset Master 255\namixer -q sset TV-Set 30\ncadenced --config /t/config.json\n"; status != 0 || calls != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, calls, want)
	}
	times, _ := feeds(t, c)
	freed := stamp(t, c, "/t/freed")
	// headroom for a loaded host: the feeder polls every 0.2 s
	if len(times) == 0 || times[0] < freed || times[0] > freed+1 {
		t.Fatalf("fed at %v, the holder let go at %.2f", times, freed)
	}
	if strings.Contains(log, "board_control_failed") || strings.Count(log, "event=watchdog_opened") != 1 {
		t.Fatalf("log:\n%s", log)
	}
}

// a holder that keeps the watchdog past the feeder's 8 s fails the prestart (the feeder gone with it), and the
// wrapper's next try 2 s later takes it
func TestHookRetriesWhileTheWatchdogStaysBusy(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	proc(c, 400, "watchdogd", `watchdogd\000`, "")
	holds(c, 400)
	stampFeeds(c)
	status, _, log := supervise(t, c, "", "sleep 9.5\n"+stampAt("/t/freed")+"rm -rf /p/400\n"+started+"sleep 0.5")
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	inOrder(t, log, "event=board_control_failed code=watchdog reason=busy", "event=watchdog_opened", "event=start")
	if strings.Count(log, "event=watchdog_opened") != 1 || strings.Count(log, "event=start") != 1 {
		t.Fatalf("log:\n%s", log)
	}
	if times, _ := feeds(t, c); len(times) == 0 || times[0] < stamp(t, c, "/t/freed") {
		t.Fatalf("fed at %v while the holder had it", times)
	}
}

// holders lists, in a test script, the real processes that hold the watchdog on their fd 3
const holders = `for p in /proc/[0-9]*; do [ ! $p/fd/3 -ef /t/watchdog ] || echo ${p#/proc/}; done`

// the feeder writes one byte, never the magic close's V, at once and then every 2 s while cadence-run lives, and it
// alone holds the watchdog: its naps do not, so none can keep it open after the feeder is gone
func TestHookFeederFeedsWhileCadenceRunLives(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stampFeeds(c)
	status, _, log := supervise(t, c, "", started+"sleep 1\n"+holders+" > /t/holders1\nsleep 1.3\n"+holders+" > /t/holders2\nsleep 2.7")
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	times, fed := feeds(t, c)
	if len(times) < 3 || len(fed) < 3 || strings.Contains(fed, "V") {
		t.Fatalf("fed %q at %v", fed, times)
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i] - times[i-1]; gap < 1.5 || gap > 3 {
			t.Fatalf("fed at %v: not every 2 s", times)
		}
	}
	inOrder(t, log, "event=watchdog_opened", "event=start", "event=stopped")
	feeder := strconv.Itoa(feederOf(t, c, "/t/run/watchdog.pid")) + "\n"
	if got := string(c.MustSh("cat /t/holders1 /t/holders2")); got != feeder+feeder {
		t.Fatalf("holding the watchdog: %q, the feeder is %s", got, feeder)
	}
}

// the feeder's grace ends at its second since boot: no feed after it, and the feeder goes without a magic close
func TestHookFeederStarvesOnceTheGraceIsOver(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stampFeeds(c)
	out, errOut, code := c.Sh(launch("", "pid")+started+"sleep 0.5\nkill -TERM $pid; wait $pid\ncp /t/run/watchdog.pid /t/feeder\n"+
		`read -r u r < /proc/uptime; g=$((${u%.*} + 3)); echo $g > /t/deadline; echo $g > /t/run/watchdog.grace
sleep 6
read -r f o < /t/feeder; [ ! /proc/$f/fd/3 -ef /t/watchdog ] || echo "still holding"`, nil)
	if code != 0 || len(out) != 0 {
		t.Fatalf("status %d: %s%s", code, out, errOut)
	}
	quiet(t, c)
	deadline := stamp(t, c, "/t/deadline")
	times, fed := feeds(t, c)
	if len(times) < 2 || after(times, deadline+0.5) != 0 || strings.Contains(fed, "V") {
		t.Fatalf("fed %q at %v, the grace ended at %.0f", fed, times, deadline)
	}
	feeder := strconv.Itoa(feederOf(t, c, "/t/feeder"))
	if log := string(c.MustSh("cat /t/log/cadenced.log")); !strings.Contains(log, "event=watchdog_feeder_stopped pid="+feeder+" reason=supervisor_gone") {
		t.Fatalf("log:\n%s", log)
	}
}

// fedBy splits the feeds by their byte: the times the stock fed (s) and the times the hook's feeders did (1)
func fedBy(times []float64, fed string) (stock, hook []float64) {
	for i := 0; i < min(len(times), len(fed)); i++ {
		if fed[i] == 's' {
			stock = append(stock, times[i])
		} else {
			hook = append(hook, times[i])
		}
	}
	return stock, hook
}

// a stop while the prestart waits for the stock to go (here its app, TERMed, keeps the watchdog 2 s without feeding
// it) leaves a feeder for the grace, which opens the watchdog once the app lets go: the feeds resume well inside the
// 8 s a holder may be away
func TestHookStopDuringTheStockStopLeavesAFeeder(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stampFeeds(c)
	proc(c, 100, "voice-agent", `voice-agent\000-mode=app\000-watchdog\000`, "stubborn")
	holds(c, 100)
	c.Tree(map[string]string{"/t/on/TERM-100": "touch /t/stock-termed\n( sleep 2; rm -rf /p/100 ) < /dev/null > /dev/null 2>&1 &\n"}, 0o644)
	out, errOut, code := c.Sh(`nohup sh -c 'while [ ! -e /t/stock-termed ]; do printf s >> /t/watchdog; sleep 0.5; done' < /dev/null > /dev/null 2>&1 &
sleep 1
`+launch("CADENCE_SLEEP=sleep", "pid")+waitFor("grep -q 'kill -TERM 100' /t/calls")+"sleep 0.5\nkill -TERM $pid; wait $pid\nsleep 5\n"+
		"cat /t/calls; echo ==; cat /t/log/cadenced.log", nil)
	if code != 0 {
		t.Fatalf("status %d: %s", code, errOut)
	}
	quiet(t, c)
	calls, log, _ := strings.Cut(string(out), "==\n")
	if strings.Contains(log, "event=start") || strings.Contains(calls, "kill -KILL") {
		t.Fatalf("calls\n%s\nlog\n%s", calls, log)
	}
	inOrder(t, log, "event=stopped", "event=watchdog_opened")
	times, fed := feeds(t, c)
	stock, hook := fedBy(times, fed)
	if len(stock) == 0 || len(hook) < 2 || hook[0] < stock[len(stock)-1] || hook[0]-stock[len(stock)-1] >= 8 {
		t.Fatalf("the stock fed at %v, the hook at %v", stock, hook)
	}
}

// a stop while the prestart lets an earlier cadence-run's feeder go (it takes a second to, here) starts a feeder for
// the grace in its place: the dying one no longer counts, and the feeds go on
func TestHookStopDuringTheTakeoverLeavesAFeeder(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stampFeeds(c)
	slowDate := `#!/bin/sh
read -r f o < /t/feeder
[ "$PPID" != "$f" ] || { touch /t/feeder-going; sleep 1; }
exec /bin/busybox date "$@"
`
	out, errOut, code := c.Sh(launch("", "a")+started+"sleep 0.5\nkill -TERM $a; wait $a\n"+
		"cp /t/run/watchdog.pid /t/feeder\nprintf '%s' '"+slowDate+"' > /fake/bin/date && chmod 755 /fake/bin/date\n"+
		stampAt("/t/b-started")+launch("", "b")+waitFor("[ -e /t/feeder-going ]")+"kill -TERM $b; wait $b\n"+stampAt("/t/b-stopped")+
		"sleep 5\ncp /t/run/watchdog.pid /t/last\ncat /t/log/cadenced.log", nil)
	if code != 0 {
		t.Fatalf("status %d: %s", code, errOut)
	}
	quiet(t, c)
	log := string(out)
	old, last := feederOf(t, c, "/t/feeder"), feederOf(t, c, "/t/last")
	if strings.Count(log, "event=start") != 1 || strings.Count(log, "event=stopped") != 2 || old == last || holding(c, old) || !holding(c, last) {
		t.Fatalf("feeder %d, then %d, log:\n%s", old, last, log)
	}
	inOrder(t, log, "event=watchdog_opened pid="+strconv.Itoa(old), "event=start", "event=stopped",
		"event=watchdog_opened pid="+strconv.Itoa(last))
	times, _ := feeds(t, c)
	// headroom for a loaded host over the feeder's 2 s; a holder may be away 8 s
	if after(times, stamp(t, c, "/t/b-stopped")) < 2 || maxGap(times) >= 4 {
		t.Fatalf("fed at %v, the second cadence-run stopped at %.2f", times, stamp(t, c, "/t/b-stopped"))
	}
}

// a cadence-run that dies without its stop (no grace) takes the feeds with it within a poll of the feeder, so a dead
// supervisor resets the board; its orphaned watcher starts no feeder for it
func TestHookFeederStopsWhenCadenceRunDies(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stampFeeds(c)
	out, errOut, code := c.Sh(launch("CADENCE_POLL_SECONDS=1", "pid")+started+"sleep 2.5\n"+
		"kill -KILL $pid; wait $pid\n"+stampAt("/t/died")+"sleep 5\ncat /t/log/cadenced.log", nil)
	if code != 0 {
		t.Fatalf("status %d: %s", code, errOut)
	}
	quiet(t, c)
	times, _ := feeds(t, c)
	died := stamp(t, c, "/t/died")
	if len(times) < 2 || after(times, died+0.5) != 0 {
		t.Fatalf("fed at %v, cadence-run died at %.2f", times, died)
	}
	log := string(out)
	if strings.Count(log, "event=watchdog_opened") != 1 || strings.Contains(log, "watchdog_feeder_restarted") {
		t.Fatalf("log:\n%s", log)
	}
	inOrder(t, log, "event=watchdog_opened", "event=watchdog_feeder_stopped pid=", "reason=supervisor_gone")
}

// an upgrade: the old cadence-run's stop leaves its feeder feeding through the grace, and the next one's prestart
// TERMs it and opens the watchdog with a feeder of its own, the feeds never more than 3 s apart
func TestHookStopGrantsGraceAndNextPrestartTakesOver(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stampFeeds(c)
	out, errOut, code := c.Sh(launch("", "a")+started+"sleep 1\n"+
		"kill -TERM $a; wait $a\n"+stampAt("/t/a-stopped")+"cp /t/run/watchdog.pid /t/a-feeder\n"+
		"sleep 4.5\n"+stampAt("/t/b-started")+
		launch("", "b")+waitFor(`[ "$(grep -c '^cadenced' /t/calls)" -ge 2 ]`)+"sleep 0.5\n"+
		"cp /t/run/watchdog.pid /t/b-feeder\n[ -e /t/run/watchdog.grace ] && echo grace-left || echo no-grace\n"+
		"sleep 2.5\nkill -TERM $b; wait $b\necho ==; cat /t/log/cadenced.log", nil)
	if code != 0 {
		t.Fatalf("status %d: %s", code, errOut)
	}
	quiet(t, c)
	grace, log, _ := strings.Cut(string(out), "==\n")
	if grace != "no-grace\n" {
		t.Fatalf("after the takeover: %s", grace)
	}
	times, _ := feeds(t, c)
	stopped, next := stamp(t, c, "/t/a-stopped"), stamp(t, c, "/t/b-started")
	if after(times, stopped)-after(times, next) < 2 {
		t.Fatalf("fed at %v: not through the grace from %.2f to %.2f", times, stopped, next)
	}
	if gap := maxGap(times); gap >= 3 {
		t.Fatalf("fed at %v: a gap of %.2f s", times, gap)
	}
	old, fresh := feederOf(t, c, "/t/a-feeder"), feederOf(t, c, "/t/b-feeder")
	if old == fresh || holding(c, old) || !holding(c, fresh) {
		t.Fatalf("old feeder %d holding %v, new %d holding %v", old, holding(c, old), fresh, holding(c, fresh))
	}
	o, f := strconv.Itoa(old), strconv.Itoa(fresh)
	inOrder(t, log, "event=watchdog_opened pid="+o, "event=stopped", "event=watchdog_feeder_stopped pid="+o+" reason=term",
		"event=watchdog_taken_over pid="+o, "event=watchdog_opened pid="+f, "event=start", "event=stopped")
}

// the earlier feeder is let go only once the stock is out of the way: a stock process that will not stop fails the
// next cadence-run's prestart with that feeder still feeding, and the takeover follows when the process is gone
func TestHookTakesOverOnlyOnceTheStockIsGone(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stampFeeds(c)
	c.Tree(map[string]string{"/t/on/KILL-100": "if [ -e /t/killed ]; then : > /t/behave/100; fi\ntouch /t/killed\n"}, 0o644)
	out, errOut, code := c.Sh(launch("", "a")+started+"kill -TERM $a; wait $a\ncp /t/run/watchdog.pid /t/a-feeder\n"+
		"mkdir -p /p/100 && echo unkillable > /t/behave/100 && printf 'voice-agent\\000-mode=app\\000' > /p/100/cmdline && echo voice-agent > /p/100/comm\n"+
		launch("", "b")+waitFor("grep -q board_control_failed /t/log/cadenced.log")+
		`read -r f o < /t/a-feeder; [ /proc/$f/fd/3 -ef /t/watchdog ] && echo held || echo let-go`+"\n"+
		waitFor(`[ "$(grep -c '^cadenced' /t/calls)" -ge 2 ]`)+"sleep 0.5\nkill -TERM $b; wait $b\necho ==; cat /t/log/cadenced.log", nil)
	if code != 0 {
		t.Fatalf("status %d: %s", code, errOut)
	}
	quiet(t, c)
	held, log, _ := strings.Cut(string(out), "==\n")
	if held != "held\n" {
		t.Fatalf("while the stock would not stop, the earlier feeder was %s", held)
	}
	o := strconv.Itoa(feederOf(t, c, "/t/a-feeder"))
	inOrder(t, log, "event=stopped", "event=board_control_failed code=stock reason=still_running",
		"event=stock_suspended count=1", "event=watchdog_feeder_stopped pid="+o+" reason=term", "event=watchdog_taken_over pid="+o,
		"event=watchdog_opened", "event=start")
	if times, _ := feeds(t, c); maxGap(times) >= 3 {
		t.Fatalf("fed at %v", times)
	}
}

// cadenced refused at every start (exit 3): the feeder that the first prestart started feeds on through every backoff
// and restart, the stock that came back meanwhile is stopped again before the next start, and capsula-root's dropbear
// is never signalled
func TestHookFeederSurvivesCadencedBackoff(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stampFeeds(c)
	proc(c, 150, "dropbearmulti", `dropbear\000-R\000`, "")
	c.Tree(map[string]string{"/t/cadenced.sh": `echo "cadenced $*" >> /t/calls
if [ ! -e /t/ran ]; then
	touch /t/ran
	cp /t/run/watchdog.pid /t/first-feeder
	mkdir -p /p/500 && echo voice-agent > /p/500/comm && printf 'voice-agent\000-mode=master\000' > /p/500/cmdline
fi
exit 3
`}, 0o644)
	status, calls, log := supervise(t, c, "CADENCE_BACKOFF=1", waitFor(`[ "$(grep -c '^cadenced' /t/calls)" -ge 3 ]`)+
		"sleep 1\ncp /t/run/watchdog.pid /t/last-feeder")
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	if strings.Count(calls, "cadenced --config") < 3 || strings.Count(calls, "kill -TERM 500\n") != 1 || strings.Contains(calls, " 150\n") {
		t.Fatalf("calls\n%s", calls)
	}
	first, last := feederOf(t, c, "/t/first-feeder"), feederOf(t, c, "/t/last-feeder")
	if first != last || !holding(c, last) || strings.Count(log, "event=watchdog_opened") != 1 {
		t.Fatalf("feeder %d then %d, log\n%s", first, last, log)
	}
	inOrder(t, log, "event=stock_suspended count=0", "event=start", "event=exit rc=3", "event=stock_suspended count=1",
		"event=start", "event=exit rc=3", "event=start")
	if times, _ := feeds(t, c); len(times) < 3 || maxGap(times) >= 3 {
		t.Fatalf("fed at %v", times)
	}
}

// cadence-run prestart, which the installer runs while the stock still holds the watchdog, stops the platform's
// daemons alone: the stock, its watchdog and its tgclient copy are left as they are, and nothing is logged to the file
func TestHookInstallerPrestartLeavesTheWatchdogAlone(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stock(c, "")
	proc(c, 160, "okcall", `/tmp/okcall\000`, "")
	out, errOut, code := c.Sh(env+` sh /t/bin/cadence-run prestart; echo "status $?"; cat /t/calls; ls /p; ls /tmp
[ -s /t/watchdog ] && echo fed; [ -e /t/run ] && echo run-dir; [ -e /t/log ] && echo logged; true`, nil)
	want := "status 0\n" + platformStops + "kill -TERM 160\n100\n120\n130\n300\ntgclient\n"
	if code != 0 || string(out) != want {
		t.Fatalf("status %d, got\n%s%s\nwant\n%s", code, out, errOut, want)
	}
	if !strings.Contains(string(errOut), "event=platform_stopped count=1") {
		t.Fatalf("stderr: %s", errOut)
	}
}

// cadence-run release, which the installer runs right before the stock's start, ends the feeder before it returns:
// no process with cadence-run's command line is left, the feeds stop, and the grace and pid files are gone. it TERMs,
// never KILLs, and with nothing to release it succeeds. the feeder takes a second to go here (its log line's date is
// slow), so a release that did not wait for it would return while it still holds the watchdog
func TestHookReleaseStopsTheFeeder(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stampFeeds(c)
	slowDate := `#!/bin/sh
read -r f o < /t/feeder
[ "$PPID" != "$f" ] || sleep 1
exec /bin/busybox date "$@"
`
	// the stop waits for cadence-run's watcher to have started: a TERM right as cadenced starts can come before
	// cadence-run knows the watcher's pid, which then outlives it until its next poll
	out, errOut, code := c.Sh(launch("", "pid")+started+"sleep 0.5\nkill -TERM $pid; wait $pid\n"+
		"cp /t/run/watchdog.pid /t/feeder\nprintf '%s' '"+slowDate+"' > /fake/bin/date && chmod 755 /fake/bin/date\n"+
		env+` sh /t/bin/cadence-run release 2> /t/release.err; echo "release $?"
read -r f o < /t/feeder; [ ! /proc/$f/fd/3 -ef /t/watchdog ] || echo "still holding"
for p in /proc/[0-9]*; do [ "$(tr '\000' ' ' < $p/cmdline 2>/dev/null)" != "sh /t/bin/cadence-run " ] || echo "left ${p#/proc/}"; done
`+stampAt("/t/released")+"ls /t/run\nsleep 2.5\n"+env+` sh /t/bin/cadence-run release; echo "again $?"`, nil)
	if code != 0 || string(out) != "release 0\nagain 0\n" {
		t.Fatalf("status %d: %s%s", code, out, errOut)
	}
	quiet(t, c)
	feeder := feederOf(t, c, "/t/feeder")
	if got := string(c.MustSh("cat /t/release.err")); !strings.Contains(got, "event=watchdog_released pid="+strconv.Itoa(feeder)) {
		t.Fatalf("release said %q", got)
	}
	times, _ := feeds(t, c)
	if len(times) == 0 || after(times, stamp(t, c, "/t/released")) != 0 {
		t.Fatalf("fed at %v, released at %.2f", times, stamp(t, c, "/t/released"))
	}
	if log := string(c.MustSh("cat /t/log/cadenced.log")); !strings.Contains(log, "event=watchdog_feeder_stopped pid="+strconv.Itoa(feeder)+" reason=term") {
		t.Fatalf("log:\n%s", log)
	}
}

// Master stays at 255, its stock value, and TV-Set holds the level, set before cadenced's first start; a level
// cadenced persists meanwhile reaches TV-Set at the next poll, and a poll with nothing new sets nothing
func TestHookVolumePinsMasterAndMirrorsTVSet(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	status, calls, log := supervise(t, c, "CADENCE_POLL_SECONDS=1", started+`sleep 1.5
mkdir -p /t/state && printf '{"level":12}' > /t/state/volume
sleep 2.5`)
	want := "amixer -q sset Master 255\namixer -q sset TV-Set 30\ncadenced --config /t/config.json\n" +
		"amixer -q sset Master 255\namixer -q sset TV-Set 12\n"
	if got := only(calls, "amixer", "cadenced"); status != 0 || got != want {
		t.Fatalf("status %d, calls\n%s\nwant\n%s", status, got, want)
	}
	inOrder(t, log, "event=volume_applied level=30", "event=start", "event=volume_applied level=12", "event=stopped")
}

// the ring's 36 channels and the clock's 30 are turned off and nothing else; a missing one fails the blank, the rest
// turned off all the same
func TestHookBlankZeroesRingAndClock(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	c.MustSh("mkdir -p /t/sys/class/leds/other && echo 7 > /t/sys/class/leds/other/brightness")
	levels := "cat /t/sys/class/leds/top-led*/brightness /t/sys/class/leds/clock-led*/brightness | sort | uniq -c | tr -s ' '; " +
		"cat /t/sys/class/leds/other/brightness"
	out, errOut, code := c.Sh(env+` sh /t/bin/cadence-run blank; echo "status $?"; `+levels, nil)
	if code != 0 || string(out) != "status 0\n 66 0\n7\n" {
		t.Fatalf("status %d: %s%s", code, out, errOut)
	}
	c.MustSh("rm -rf /t/sys/class/leds/top-led7 && for f in /t/sys/class/leds/*-led*/brightness; do echo 9 > $f; done")
	if out, errOut, _ = c.Sh(env+` sh /t/bin/cadence-run blank; echo "status $?"; `+levels, nil); string(out) != "status 1\n 65 0\n7\n" {
		t.Fatalf("a missing led: %s%s", out, errOut)
	}
}

// stock that comes back while cadenced runs, and the platform daemons rcS starts after the launcher, are stopped again
// at the next poll, cadenced left running; the comeback is logged for the installer's check, and the level is set
// again, as what came back may have moved it
func TestHookRestopsReturningStock(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	status, calls, log := supervise(t, c, "CADENCE_POLL_SECONDS=1", started+`mkdir -p /p/500 /p/510 /p/520
printf 'voice-agent\000-mode=master\000' > /p/500/cmdline; echo voice-agent > /p/500/comm
echo btctl > /p/510/comm
echo sysmon > /p/520/comm
sleep 3`)
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	rest, found := strings.CutPrefix(calls, platformStops+"amixer -q sset Master 255\namixer -q sset TV-Set 30\ncadenced --config /t/config.json\n")
	if !found {
		t.Fatalf("calls\n%s", calls)
	}
	inOrder(t, rest, "kill -TERM 500\n", "kill -TERM 510\n", "amixer -q sset Master 255\namixer -q sset TV-Set 30\n")
	inOrder(t, rest, platformStops+"kill -TERM 520\n", "amixer -q sset Master 255\namixer -q sset TV-Set 30\n")
	if strings.Contains(rest, "KILL") || strings.Count(log, "event=start") != 1 || strings.Contains(log, "event=exit") {
		t.Fatalf("calls\n%s\nlog\n%s", calls, log)
	}
	n := 0
	for _, l := range strings.Split(log, "\n") {
		if _, count, ok := strings.Cut(l, "event=stock_resuspended count="); ok {
			v, err := strconv.Atoi(count)
			if err != nil {
				t.Fatalf("line %q", l)
			}
			n += v
		}
	}
	if n != 2 || !strings.Contains(log, "event=platform_stopped count=1") {
		t.Fatalf("log:\n%s", log)
	}
}

// a feeder that dies while cadence-run lives is started again at the next poll
func TestHookRestartsADeadFeeder(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	stampFeeds(c)
	status, _, log := supervise(t, c, "CADENCE_POLL_SECONDS=1", started+"sleep 0.5\ncp /t/run/watchdog.pid /t/first\n"+
		"read -r f o < /t/run/watchdog.pid; kill -TERM $f\nsleep 2.5\ncp /t/run/watchdog.pid /t/second")
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	first, second := feederOf(t, c, "/t/first"), feederOf(t, c, "/t/second")
	if first == second || !holding(c, second) {
		t.Fatalf("feeder %d, then %d holding %v", first, second, holding(c, second))
	}
	f, s := strconv.Itoa(first), strconv.Itoa(second)
	inOrder(t, log, "event=watchdog_opened pid="+f, "event=start", "event=watchdog_feeder_stopped pid="+f+" reason=term",
		"event=watchdog_opened pid="+s, "event=watchdog_feeder_restarted pid="+s, "event=stopped")
	// a feed at most 2 s before the kill, then up to a poll and the open: well inside the 10 s timeout
	if times, _ := feeds(t, c); maxGap(times) >= 4 {
		t.Fatalf("fed at %v", times)
	}
}

// a pid file whose pid now runs something else (it holds no watchdog) is never signalled, by a prestart or a release:
// on a real board that pid could be capsula-root's dropbear
func TestHookNeverSignalsAStalePidFile(t *testing.T) {
	t.Parallel()
	c := hookBox(t)
	c.MustSh(`nohup sleep 60 < /dev/null > /dev/null 2>&1 & echo $! > /t/other; mkdir -p /t/run && echo "$(cat /t/other) 1" > /t/run/watchdog.pid`)
	other := strings.TrimSpace(string(c.MustSh("cat /t/other")))
	status, _, log := supervise(t, c, "", started)
	if status != 0 || !strings.Contains(log, "event=watchdog_opened") || strings.Contains(log, "taken_over") {
		t.Fatalf("status %d, log\n%s", status, log)
	}
	c.MustSh(`echo "$(cat /t/other) 1" > /t/run/watchdog.pid`)
	if out, errOut, code := c.Sh(env+` sh /t/bin/cadence-run release; echo "status $?"; kill -0 `+other+` && echo alive`, nil); code != 0 ||
		string(out) != "status 0\nalive\n" {
		t.Fatalf("release: %s%s", out, errOut)
	}
}
