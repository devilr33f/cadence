package installer_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/install/busyboxtest"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

// the wrapper runs under busybox sh with its home at /t, pickle's hook beside it, the lights under /t/dev and
// /t/leds, and fake amixer and cadenced that log what they were given
const env = "CADENCE_HOME=/t CADENCE_AMIXER=/fake/amixer CADENCE_DEV=/t/dev CADENCE_LEDS=/t/leds"

func wrapperBox(t *testing.T) *busyboxtest.Container {
	t.Helper()
	script, err := os.ReadFile("../../../internal/install/files/cadence-run")
	if err != nil {
		t.Fatal(err)
	}
	hook, err := os.ReadFile("../files/board-hook")
	if err != nil {
		t.Fatal(err)
	}
	c := busyboxtest.Start(t)
	c.Tree(map[string]string{
		"/t/bin/cadence-run": string(script),
		"/t/bin/board-hook":  string(hook),
		"/fake/amixer":       "#!/bin/sh\necho \"amixer $*\" >> /t/calls\n[ ! -e /t/amixer-fails ]\n",
		"/t/bin/cadenced":    "#!/bin/sh\n. /t/cadenced.sh\n",
	}, 0o755)
	return c
}

func level(t *testing.T, c *busyboxtest.Container, content *string) string {
	t.Helper()
	c.MustSh("rm -rf /t/state && mkdir -p /t/state")
	if content != nil {
		c.Put("/t/state/volume", []byte(*content), "600")
	}
	out, errOut, code := c.Sh(env+" sh /t/bin/cadence-run level", nil)
	if code != 0 {
		t.Fatalf("level: status %d: %s", code, errOut)
	}
	return string(out)
}

// the persisted level exactly as cadenced writes it, else 30, the board's default: a file a sed cannot read for
// certain never raises the volume past what was set (rule 7)
func TestWrapperLevel(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	if got := level(t, c, nil); got != "30\n" {
		t.Fatalf("missing file: %q", got)
	}
	s := func(v string) *string { return &v }
	for in, want := range map[*string]string{
		s(`{"level":42}`): "42", s("{\"level\":42}\n"): "42", s(" { \"level\" :\t7 } "): "7", s(`{"level":0}`): "0",
		s(`{"level":100}`): "100", s(`{"level":101}`): "30", s(`{"level":-1}`): "30", s(`{"level":007}`): "30",
		s(`{"level":4.5}`): "30", s(`{"level":"42"}`): "30", s(`{"level":42,"x":1}`): "30", s(`{"LEVEL":42}`): "30",
		s("42\n"): "30", s(""): "30", s("{\"level\":42}\n\n"): "30", s("{\"level\":\n42}"): "30",
		s("{\"level\":4\x002}"): "30", s("{\"level\":42}\x00"): "30", s(strings.Repeat(" ", 60) + `{"level":42}`): "30",
		s("\xff\xfe\x00garbage"): "30", s(`{"level":42}{"level":99}`): "30", s(`{"level":1000}`): "30",
	} {
		if got := level(t, c, in); got != want+"\n" {
			t.Errorf("%q -> %q, want %s", *in, got, want)
		}
	}
	// what cadenced and the installer write
	for _, n := range []int{0, 30, 55, 100} {
		in := string(volume.EncodeLevel(n))
		if got := level(t, c, &in); got != fmt.Sprintf("%d\n", n) {
			t.Errorf("%s -> %q", in, got)
		}
	}
	c.MustSh("rm -rf /t/state && mkdir -p /t/state/volume")
	if out, _, _ := c.Sh(env+" sh /t/bin/cadence-run level", nil); string(out) != "30\n" {
		t.Errorf("a directory: %q", out)
	}
}

func TestWrapperBlanksTheLights(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	c.MustSh(`mkdir -p /t/dev /t/leds/pickle:white:clockA0 /t/leds/pickle:white:clockDEGREE /t/leds/other &&
		head -c 64 /dev/urandom > /t/dev/fb1 && head -c 64 /dev/urandom > /t/dev/fb0 &&
		echo 255 > /t/leds/pickle:white:clockA0/brightness && echo 255 > /t/leds/pickle:white:clockDEGREE/brightness &&
		echo 255 > /t/leds/other/brightness`)
	c.MustSh(env + " sh /t/bin/cadence-run blank")
	got := string(c.MustSh(`od -An -v -tx1 -N48 /t/dev/fb1 | tr -d ' \n'; echo; od -An -v -tx1 -N18 /t/dev/fb0 | tr -d ' \n'; echo
		cat /t/leds/pickle:white:clockA0/brightness /t/leds/pickle:white:clockDEGREE/brightness /t/leds/other/brightness`))
	want := strings.Repeat("00", 48) + "\n" + strings.Repeat("00", 18) + "\n0\n0\n255\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// without the nodes there is nothing to blank, and that is no failure
	if _, errOut, code := c.Sh("CADENCE_HOME=/t CADENCE_DEV=/nowhere CADENCE_LEDS=/nowhere sh /t/bin/cadence-run blank", nil); code != 0 {
		t.Fatalf("status %d: %s", code, errOut)
	}
	if _, _, code := c.Sh(env+" sh /t/bin/cadence-run nonsense", nil); code != 64 {
		t.Fatalf("an unknown mode: status %d", code)
	}
}

// before every start the level is set; cadenced runs with its config, its output in the log; a quick exit (a board
// that refuses, exit 3) backs off, doubling, so init's respawn never spins
func TestWrapperSetsTheLevelBeforeEachStartAndBacksOff(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	c.Tree(map[string]string{
		"/t/state/volume": `{"level":42}`,
		"/t/cadenced.sh":  "echo \"cadenced $* at $(date +%s)\" >> /t/calls\necho 'refused' >&2\nexit 3\n",
	}, 0o600)
	out, errOut, code := c.Sh(env+` CADENCE_BACKOFF=1 sh /t/bin/cadence-run & pid=$!
sleep 4; kill -TERM $pid; wait $pid; echo "status $?"; cat /t/calls; echo ==; cat /t/log/cadenced.log`, nil)
	if code != 0 {
		t.Fatalf("status %d: %s", code, errOut)
	}
	text := string(out)
	parts := strings.SplitN(text, "==\n", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "status 0\n") {
		t.Fatalf("output:\n%s", text)
	}
	calls := strings.Split(strings.TrimSpace(strings.TrimPrefix(parts[0], "status 0\n")), "\n")
	// 0 s, 1 s, 3 s: three starts in four seconds, each after the level
	if len(calls) != 6 {
		t.Fatalf("calls:\n%s", parts[0])
	}
	var starts []int
	for i, call := range calls {
		if i%2 == 0 {
			if call != "amixer -q sset Master 42%" {
				t.Fatalf("call %d = %q", i, call)
			}
			continue
		}
		var at int
		if _, err := fmt.Sscanf(call, "cadenced --config /t/config.json at %d", &at); err != nil {
			t.Fatalf("call %d = %q", i, call)
		}
		starts = append(starts, at)
	}
	if d1, d2 := starts[1]-starts[0], starts[2]-starts[1]; d1 < 1 || d1 > 2 || d2 < 2 || d2 > 3 {
		t.Fatalf("starts %v: the backoff does not double from 1 s", starts)
	}
	log := parts[1]
	for _, want := range []string{"event=volume_applied level=42", "event=start", "refused", "event=exit rc=3 ran_s=",
		"event=restart delay_s=1", "event=restart delay_s=2", "event=stopped"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Count(log, "event=volume_applied") != 1 {
		t.Errorf("a steady level is logged once:\n%s", log)
	}
}

// TERM stops cadenced and blanks the lights; a level amixer refuses is logged and never stops the start
func TestWrapperStopsCadencedOnTerm(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	c.Tree(map[string]string{
		"/t/cadenced.sh": "trap 'echo term >> /t/calls; exit 0' TERM\necho started >> /t/calls\nwhile :; do sleep 1; done\n",
	}, 0o600)
	c.MustSh("mkdir -p /t/dev && touch /t/amixer-fails && head -c 48 /dev/urandom > /t/dev/fb1")
	out, _, _ := c.Sh(env+` sh /t/bin/cadence-run & pid=$!
sleep 2; kill -TERM $pid; wait $pid; echo "status $?"; cat /t/calls; od -An -tx1 -N4 /t/dev/fb1; cat /t/log/cadenced.log`, nil)
	text := string(out)
	for _, want := range []string{"status 0\n", "amixer -q sset Master 30%\nstarted\nterm\n", " 00 00 00 00\n", "event=volume_failed", "event=stopped"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
}

// a run of a stable length resets the backoff; the log is cut at its limit into log.1 while cadenced runs
func TestWrapperResetsTheBackoffAndRotatesTheLog(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	// each run stamps its start: the log rotates every run, so earlier restart lines do not survive in it
	c.Tree(map[string]string{
		"/t/cadenced.sh": "date +%s >> /t/runs; head -c 300 /dev/zero | tr '\\000' x; echo; sleep 2; exit 1\n",
	}, 0o600)
	out, _, _ := c.Sh(env+` CADENCE_BACKOFF=1 CADENCE_STABLE_SECONDS=1 CADENCE_LOG_LIMIT=200 CADENCE_POLL_SECONDS=1 sh /t/bin/cadence-run & pid=$!
sleep 12; kill -TERM $pid; wait $pid; cat /t/runs; echo ---; cat /t/log/cadenced.log /t/log/cadenced.log.1`, nil)
	text := string(out)
	runs, _, _ := strings.Cut(text, "---")
	var starts []int
	for _, f := range strings.Fields(runs) {
		var s int
		if _, err := fmt.Sscan(f, &s); err == nil {
			starts = append(starts, s)
		}
	}
	// a 2 s run plus a 1 s pause is 3 s a cycle; a backoff that doubled would make the third gap 6 s
	if len(starts) < 4 {
		t.Fatalf("too few runs to see the backoff %v:\n%s", starts, text)
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i] - starts[i-1]; gap > 4 {
			t.Errorf("starts %v: the backoff did not reset after stable runs:\n%s", starts, text)
			break
		}
	}
	if strings.Contains(text, "delay_s=2") {
		t.Errorf("the backoff doubled after a stable run:\n%s", text)
	}
	if !strings.Contains(text, strings.Repeat("x", 300)) {
		t.Errorf("cadenced's output was not kept in log.1:\n%s", text)
	}
}

// a cadenced that ignores TERM is killed about 3 s later, and the wrapper still stops cleanly
func TestWrapperKillsACadencedThatIgnoresTerm(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	c.Tree(map[string]string{
		"/t/cadenced.sh": "trap '' TERM\necho $$ > /t/cadenced.pid\nwhile :; do sleep 1; done\n",
	}, 0o600)
	out, _, _ := c.Sh(env+` sh /t/bin/cadence-run & pid=$!
sleep 2; start=$(date +%s); kill -TERM $pid; wait $pid; echo "status $? after $(($(date +%s) - start)) s"
if kill -0 "$(cat /t/cadenced.pid)" 2>/dev/null; then echo alive; else echo gone; fi; cat /t/log/cadenced.log`, nil)
	text := string(out)
	var status, took int
	if _, err := fmt.Sscanf(text, "status %d after %d s", &status, &took); err != nil || status != 0 || took < 2 || took > 5 {
		t.Fatalf("output:\n%s", text)
	}
	if !strings.Contains(text, "\ngone\n") || !strings.Contains(text, "event=stopped") {
		t.Fatalf("output:\n%s", text)
	}
}

// pickle keeps no stock to suspend: the installer's prestart succeeds and does nothing
func TestWrapperPrestartDoesNothing(t *testing.T) {
	t.Parallel()
	c := wrapperBox(t)
	if out, errOut, code := c.Sh(env+" sh /t/bin/cadence-run prestart; echo \"status $?\"; ls /t", nil); code != 0 || string(out) != "status 0\nbin\n" {
		t.Fatalf("status %d: %s%s", code, out, errOut)
	}
}
