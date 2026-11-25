package install

import (
	"os"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/install/busyboxtest"
)

const runEnv = "CADENCE_HOME=/t CADENCE_AMIXER=/fake/amixer CADENCE_POLL_SECONDS=1"

func runBox(t *testing.T, hook string) *busyboxtest.Container {
	t.Helper()
	script, err := os.ReadFile("files/cadence-run")
	if err != nil {
		t.Fatal(err)
	}
	c := busyboxtest.Start(t)
	files := map[string]string{
		"/t/bin/cadence-run": string(script),
		"/fake/amixer":       "#!/bin/sh\necho \"amixer $*\" >> /t/calls\n",
		"/t/bin/cadenced":    "#!/bin/sh\n. /t/cadenced.sh\n",
	}
	if hook != "" {
		files["/t/bin/board-hook"] = hook
	}
	c.Tree(files, 0o755)
	return c
}

func TestCadenceRunLevelWithStockApplets(t *testing.T) {
	t.Parallel()
	c := runBox(t, "")
	c.StockBin("/stock-bin", busyboxtest.CapsulaBootApplets)
	s := func(v string) *string { return &v }
	cases := []struct {
		name string
		in   *string
		want string
	}{
		{"plain", s(`{"level":7}`), "7"},
		{"max with newline", s("{\"level\":100}\n"), "100"},
		{"blanks around tokens", s(" { \"level\" : 42 } "), "42"},
		{"over 100", s(`{"level":101}`), "30"},
		{"negative", s(`{"level":-1}`), "30"},
		{"absent", nil, "30"},
		{"65 bytes", s(strings.Repeat(" ", 54) + `{"level":5}`), "30"},
		{"two lines", s("{\"level\":5}\n\n"), "30"},
		{"embedded nul", s("{\"level\":5}\x00"), "30"},
		{"extra key", s(`{"level":5,"x":1}`), "30"},
		{"blank inside a token", s(`{"level":4 2}`), "30"},
		{"leading zeros", s(`{"level":007}`), "30"},
	}
	for _, tc := range cases {
		c.MustSh("rm -rf /t/state && mkdir -p /t/state")
		if tc.in != nil {
			c.Put("/t/state/volume", []byte(*tc.in), "600")
		}
		out, errOut, code := c.Sh("PATH=/stock-bin CADENCE_HOME=/t sh /t/bin/cadence-run level", nil)
		if code != 0 || string(out) != tc.want+"\n" {
			t.Errorf("%s: status %d, out %q, err %q, want %s", tc.name, code, out, errOut, tc.want)
		}
	}
}

func TestCadenceRunUsesBoardVolume(t *testing.T) {
	t.Parallel()
	hook := "board_blank() { return 0; }\nboard_prestart() { return 0; }\nboard_volume() { echo \"vol $1\" >> /t/vol; }\n"
	run := runEnv + ` sh /t/bin/cadence-run & pid=$!
sleep 2; kill -TERM $pid; wait $pid; echo "status $?"; cat /t/vol 2>&1; echo ==; cat /t/calls 2>&1`
	files := map[string]string{"/t/state/volume": `{"level":12}`, "/t/cadenced.sh": "while :; do sleep 1; done\n"}

	c := runBox(t, hook)
	c.Tree(files, 0o600)
	out, _, _ := c.Sh(run, nil)
	vol, calls, _ := strings.Cut(string(out), "==\n")
	if !strings.Contains(vol, "vol 12\n") || strings.Contains(calls, "amixer") {
		t.Fatalf("with a hook:\n%s", out)
	}

	c = runBox(t, "board_blank() { return 0; }\nboard_prestart() { return 0; }\n")
	c.Tree(files, 0o600)
	out, _, _ = c.Sh(run, nil)
	if !strings.Contains(string(out), "amixer -q sset Master 12%\n") {
		t.Fatalf("without a hook:\n%s", out)
	}
}

func TestCadenceRunStopCallsBoardStop(t *testing.T) {
	t.Parallel()
	hook := "board_blank() { echo blank >> /t/calls; }\nboard_prestart() { return 0; }\nboard_stop() { echo stop >> /t/calls; }\n"
	c := runBox(t, hook)
	c.Tree(map[string]string{"/t/cadenced.sh": "trap 'echo exited >> /t/calls; exit 0' TERM\nwhile :; do sleep 1; done\n"}, 0o600)
	out, _, _ := c.Sh(runEnv+` sh /t/bin/cadence-run & pid=$!
sleep 2; kill -TERM $pid; wait $pid; echo "status $?"; grep -v amixer /t/calls`, nil)
	// the blank before the start comes first; at the stop: cadenced exits, then board_stop, then board_blank
	want := "status 0\nblank\nexited\nstop\nblank\n"
	if string(out) != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestCadenceRunRelease(t *testing.T) {
	t.Parallel()
	c := runBox(t, "board_release() { echo released > /t/marker; return 7; }\n")
	out, _, _ := c.Sh(runEnv+" sh /t/bin/cadence-run release; echo \"status $?\"; cat /t/marker", nil)
	if string(out) != "status 7\nreleased\n" {
		t.Fatalf("got %q", out)
	}
	c = runBox(t, "")
	if _, errOut, code := c.Sh(runEnv+" sh /t/bin/cadence-run release", nil); code != 0 {
		t.Fatalf("default release: status %d: %s", code, errOut)
	}
}
