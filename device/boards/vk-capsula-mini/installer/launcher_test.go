package installer_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/install/busyboxtest"
)

// the launcher runs as rcS and the installer run it, on the stock busybox's applets alone (PATH=/stock-bin), with
// cadence's home at /t and a fake stock S97 that logs its argument to /t/stock and exits, as the real one does
const launcherEnv = "PATH=/stock-bin CADENCE_HOME=/t CADENCE_STOCK_S97=/fake/S97voice-agent.sh"

const pidFile = "/run/cadence/cadence-run.pid"

// cadence-run stays a shell (so start-stop-daemon knows it by -x /bin/sh) until a TERM, which it logs
const fakeCadenceRun = `trap 'echo term >> /t/runs; exit 0' TERM
echo "run $$" >> /t/runs
while :; do sleep 1 & wait $!; done
`

func launcherBox(t *testing.T, tree bool) *busyboxtest.Container {
	t.Helper()
	c := busyboxtest.Start(t)
	c.StockBin("/stock-bin", busyboxtest.CapsulaBootApplets)
	files := map[string]string{
		"/etc/init.d/S97voice-agent.sh": read(t, "files/S97voice-agent.sh"),
		"/fake/S97voice-agent.sh":       "#!/bin/sh\necho \"$1\" >> /t/stock\nexit 0\n",
	}
	if tree {
		files["/t/bin/cadence-run"] = fakeCadenceRun
	}
	c.Tree(files, 0o755)
	c.MustSh("mkdir -p /t && touch /t/stock /t/runs")
	return c
}

// the pid in the launcher's pid file, and that process's command line with its NULs as spaces
func supervisor(t *testing.T, c *busyboxtest.Container) (int, string) {
	t.Helper()
	out := strings.TrimSpace(string(c.MustSh(`p=$(cat ` + pidFile + `) && echo "$p" && tr '\000' ' ' < /proc/$p/cmdline`)))
	pid, cmdline, _ := strings.Cut(out, "\n")
	n, err := strconv.Atoi(pid)
	if err != nil {
		t.Fatalf("pid file: %q", out)
	}
	return n, strings.TrimSpace(cmdline)
}

// start returns at once, leaving cadence-run detached (its own session, stdin /dev/null) under the pid the file names;
// a second start leaves it alone
func TestLauncherStartsCadenceRunDetached(t *testing.T) {
	c := launcherBox(t, true)
	began := time.Now()
	out, errOut, code := c.Sh(launcherEnv+" sh /etc/init.d/S97voice-agent.sh start", nil)
	if code != 0 || time.Since(began) > 3*time.Second {
		t.Fatalf("start: status %d after %s: %s%s", code, time.Since(began), out, errOut)
	}
	c.MustSh(`for i in 1 2 3 4 5 6 7 8 9 10; do grep -q run /t/runs && break; sleep 0.2; done`)
	pid, cmdline := supervisor(t, c)
	if cmdline != "/bin/sh /t/bin/cadence-run" {
		t.Fatalf("pid %d runs %q", pid, cmdline)
	}
	if got := strings.TrimSpace(string(c.MustSh("readlink /proc/" + strconv.Itoa(pid) + "/fd/0"))); got != "/dev/null" {
		t.Fatalf("cadence-run's stdin is %s", got)
	}
	if _, errOut, code := c.Sh(launcherEnv+" sh /etc/init.d/S97voice-agent.sh start", nil); code != 0 {
		t.Fatalf("a second start: status %d: %s", code, errOut)
	}
	if again, _ := supervisor(t, c); again != pid || string(c.MustSh("sleep 1; cat /t/runs /t/stock")) != "run "+strconv.Itoa(pid)+"\n" {
		t.Fatalf("a second start: pid %d, then %d:\n%s", pid, again, c.MustSh("cat /t/runs /t/stock"))
	}
}

// with no cadence tree, or with it disabled, the stock S97 runs instead, as a program of its own: sourced as rcS
// sources it (set start, in a subshell), the launcher returns to the shell after the stock's exit
func TestLauncherFallsBackToStock(t *testing.T) {
	for name, tree := range map[string]bool{"no tree": false, "disabled": true} {
		t.Run(name, func(t *testing.T) {
			c := launcherBox(t, tree)
			if tree {
				c.MustSh("touch /t/disabled")
			}
			out := string(c.MustSh(launcherEnv + " sh -c '( trap - INT QUIT TSTP; set start; . /etc/init.d/S97voice-agent.sh; echo after )'"))
			if !strings.HasSuffix(out, "after\n") {
				t.Fatalf("the sourced launcher did not return:\n%s", out)
			}
			if got := string(c.MustSh("cat /t/stock /t/runs; [ ! -e " + pidFile + " ] && echo no-pid-file")); got != "start\nno-pid-file\n" {
				t.Fatalf("stock and cadence-run:\n%s", got)
			}
			out = string(c.MustSh(launcherEnv + " sh -c '( set stop; . /etc/init.d/S97voice-agent.sh; echo after )'; cat /t/stock"))
			if out != "after\nstart\nstop\n" {
				t.Fatalf("stop:\n%s", out)
			}
		})
	}
}

// stop TERMs the cadence-run the pid file names, and nothing of the stock's; restart waits for it to go before it
// starts the next
func TestLauncherStopTermsCadenceRun(t *testing.T) {
	c := launcherBox(t, true)
	c.MustSh(launcherEnv + " sh /etc/init.d/S97voice-agent.sh start")
	c.MustSh(`for i in 1 2 3 4 5 6 7 8 9 10; do grep -q run /t/runs && break; sleep 0.2; done`)
	pid, _ := supervisor(t, c)
	c.MustSh(launcherEnv + " sh /etc/init.d/S97voice-agent.sh stop")
	// the container's pid 1 reaps nothing, so a cadence-run that exited stays a zombie
	alive := `{ s=$(cut -d' ' -f3 /proc/` + strconv.Itoa(pid) + `/stat 2>/dev/null) && [ "$s" != Z ]; }`
	got := string(c.MustSh(`for i in 1 2 3 4 5 6 7 8 9 10; do ` + alive + ` || break; sleep 0.2; done; ` +
		alive + ` && echo alive; cat /t/runs /t/stock`))
	if got != "run "+strconv.Itoa(pid)+"\nterm\n" {
		t.Fatalf("after stop:\n%s", got)
	}
	c.MustSh(launcherEnv + " sh /etc/init.d/S97voice-agent.sh start")
	c.MustSh(`for i in 1 2 3 4 5 6 7 8 9 10; do [ $(grep -c run /t/runs) = 2 ] && break; sleep 0.2; done`)
	second, _ := supervisor(t, c)
	c.MustSh(launcherEnv + " sh /etc/init.d/S97voice-agent.sh restart")
	c.MustSh(`for i in 1 2 3 4 5 6 7 8 9 10; do [ $(grep -c run /t/runs) = 3 ] && break; sleep 0.2; done`)
	third, cmdline := supervisor(t, c)
	if third == second || cmdline != "/bin/sh /t/bin/cadence-run" || string(c.MustSh("cat /t/stock")) != "" {
		t.Fatalf("restart: %d then %d %q:\n%s", second, third, cmdline, c.MustSh("cat /t/runs /t/stock"))
	}
}

// each stub answers as voice-agent reads "no update", exit 1, with nothing on stdout (voice-agent parses update-mode
// lines as progress) and one line to syslog
func TestStubsAnswerNoUpdate(t *testing.T) {
	c := busyboxtest.Start(t)
	c.StockBin("/stock-bin", busyboxtest.CapsulaBootApplets)
	c.Tree(map[string]string{
		"/s/fwupdater":   read(t, "files/fwupdater.stub"),
		"/s/swupdate.sh": read(t, "files/swupdate.sh.stub"),
		"/fake/logger":   "#!/bin/sh\necho \"logger $*\" >> /t/syslog\n",
	}, 0o755)
	c.MustSh("mkdir -p /t")
	for _, run := range []string{"/s/fwupdater --check --logtosyslog --session_file /x", "/s/swupdate.sh /tmp/update.img reboot"} {
		c.MustSh("rm -f /t/syslog")
		out, errOut, code := c.Sh("PATH=/fake:/stock-bin "+run, nil)
		if code != 1 || len(out) != 0 || len(errOut) != 0 {
			t.Errorf("%s: status %d, stdout %q, stderr %q", run, code, out, errOut)
		}
		if logged := string(c.MustSh("cat /t/syslog")); strings.Count(logged, "\n") != 1 || !strings.HasPrefix(logged, "logger -t cadence vendor ota disabled") {
			t.Errorf("%s logged %q", run, logged)
		}
	}
}
