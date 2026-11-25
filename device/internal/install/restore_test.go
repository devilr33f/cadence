package install

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func restore(t *testing.T, f *fake, o Options) (string, error) {
	t.Helper()
	var out bytes.Buffer
	o.Out = &out
	err := New(f, f.p, o).RestoreStock(context.Background())
	return out.String(), err
}

func restoreOpts(t *testing.T, f *fake) Options {
	o := midiOpts(t, f)
	o.Bundle = nil
	return o
}

// --restore-stock --ota-blocked: cadence's line out of init (every other byte as it was), v2 stopped, the lights
// blanked, then the stock's start script run detached; never its boot routine. v2's tree stays, and a later install
// starts v2 again
func TestRestoreStockResumesTheStock(t *testing.T) {
	f := midiSpeaker(t, true)
	mustInstallMidi(t, f)
	f.ops = nil
	out, err := restore(t, f, restoreOpts(t, f))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := string(f.get("/etc/inittab")); got != stopped || f.rootRW {
		t.Fatalf("inittab =\n%s\nwant it without cadence's line, byte for byte", got)
	}
	if len(matching(New(f, f.p, Options{}).v2Processes(), procsOf(f))) != 0 || len(f.pidsOf(stockLauncher)) != 1 {
		t.Fatalf("processes: %+v", f.procs)
	}
	ops := f.changes()
	swap := slices.Index(ops, "swap-inittab /data/cadence/.install/inittab.next")
	term := slices.IndexFunc(ops, func(o string) bool { return strings.HasPrefix(o, "kill -TERM") })
	blank := slices.Index(ops, "blank /data/cadence/bin/cadence-run")
	start := slices.Index(ops, "start-stop-daemon "+stockStart)
	if swap < 0 || term < swap || blank < term || start < blank {
		t.Fatalf("commands:\n%s", strings.Join(ops, "\n"))
	}
	if got := string(f.get("/data/cadence/backup/inittab.20261109T120000Z.restore-stock")); got != strings.Replace(stopped, adbd+"\n", adbd+"\n"+v2Line+"\n", 1) {
		t.Fatalf("backup %q", got)
	}
	for _, op := range ops {
		if strings.Contains(op, "onboot") {
			t.Fatalf("ran the stock's boot routine: %s", op)
		}
	}
	if got := string(f.get("/data/cadence/install.state")); got != "stop-v1\nsuspend\nv1-moved\nstage\nenroll\n" {
		t.Fatalf("install.state %q", got)
	}
	if string(f.get("/data/cadence/bin/cadenced")) != "v2 cadenced" || !f.has("/data/cadence/config.json") || f.has("/data/cadence/.install") {
		t.Fatal("v2's tree did not stay, or the work dir did")
	}
	if !strings.Contains(out, "verify: recovery access kept, the stock assistant runs") {
		t.Fatalf("output:\n%s", out)
	}

	// once more: nothing to do, and no second stock
	f.ops = nil
	if out, err := restore(t, f, restoreOpts(t, f)); err != nil || !strings.Contains(out, "runs already") {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, op := range f.changes() {
		if strings.HasPrefix(op, "swap") || strings.HasPrefix(op, "kill") || strings.HasPrefix(op, "start-stop-daemon") {
			t.Fatalf("a second run changed: %q", f.changes())
		}
	}

	// and v2 again: suspend and start-v2 run anew, the stock suspended before init's line returns
	f.ops = nil
	mustInstallMidi(t, f)
	if got := string(f.get("/etc/inittab")); got != swapped || len(f.stockPids()) != 0 || len(f.pidsOf("/data/cadence/bin/cadenced")) != 1 {
		t.Fatalf("inittab after a second install =\n%s\nstock %v", got, f.stockPids())
	}
	ops = f.changes()
	if p, s := slices.Index(ops, "prestart /data/cadence/.install/cadence-run"), slices.Index(ops, "swap-inittab /data/cadence/.install/inittab.next"); p < 0 || s < p {
		t.Fatalf("a second install:\n%s", strings.Join(ops, "\n"))
	}
}

func procsOf(f *fake) []Proc {
	procs, _ := f.Processes(context.Background())
	return procs
}

// every refusal comes before anything changes
func TestRestoreStockRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*fake, *Options)
		want  string
	}{
		"ota not blocked": {func(_ *fake, o *Options) { o.OTABlocked = false }, "keep the router's rules that block the vendor's OTA servers"},
		"no stock to restore": {func(f *fake, _ *Options) {
			f.p = testProfile()
			f.l = layoutOf(f.p.DataRoot)
		}, "yandex-pickle keeps no stock assistant to restore"},
		"v1 in init": {func(f *fake, _ *Options) {
			f.put("/etc/inittab", []byte(strings.Replace(string(f.get("/etc/inittab")), v2Line+"\n", v2Line+"\n"+v1Line+"\n", 1)), 0o644)
		}, "v1 runs from /etc/inittab"},
		"a stranger from bin": {func(f *fake, _ *Options) {
			f.spawn(Proc{Exe: "/data/cadence/bin/cadenced", Args: []string{"cadenced", "--config", "/tmp/other.json"}}, "", 0)
		}, "runs /data/cadence/bin/cadenced, but not as cadence runs it"},
		"start not executable": {func(f *fake, _ *Options) { f.files[stockStart].mode = 0o644 }, "/opt/stock/run.sh is not executable"},
	} {
		t.Run(name, func(t *testing.T) {
			f := midiSpeaker(t, true)
			mustInstallMidi(t, f)
			f.ops = nil
			o := restoreOpts(t, f)
			tc.setup(f, &o)
			_, err := restore(t, f, o)
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(f.changes()) != 0 {
				t.Fatalf("a refused run changed: %q", f.changes())
			}
		})
	}
}

// a v2 that will not stop leaves the stock off: init no longer runs v2, and the run fails before the stock starts
func TestRestoreStockLeavesTheStockOffWhileV2Runs(t *testing.T) {
	f := midiSpeaker(t, true)
	mustInstallMidi(t, f)
	f.ghost["/data/cadence/bin/cadenced"] = true
	f.ops = nil
	_, err := restore(t, f, restoreOpts(t, f))
	if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "stop-v2: still running after KILL: cadenced") {
		t.Fatalf("err = %v", err)
	}
	if slices.ContainsFunc(f.changes(), func(o string) bool { return strings.HasPrefix(o, "start-stop-daemon") }) || len(f.stockPids()) != 0 {
		t.Fatalf("the stock started beside v2: %q", f.changes())
	}
	if !strings.Contains(string(f.get("/data/cadence/install.state")), "start-v2") {
		t.Fatal("install.state lost start-v2 though the stock never started")
	}
}

// verify needs the launcher running and recovery access kept
func TestRestoreStockVerifies(t *testing.T) {
	f := midiSpeaker(t, true)
	mustInstallMidi(t, f)
	f.noLaunch = true
	if _, err := restore(t, f, restoreOpts(t, f)); err == nil || !strings.Contains(err.Error(), "verify: /opt/stock/launcher does not run") {
		t.Fatalf("err = %v", err)
	}

	f = midiSpeaker(t, true)
	mustInstallMidi(t, f)
	o := restoreOpts(t, f)
	o.Recovery = []func(context.Context) error{func(context.Context) error {
		if len(f.pidsOf(stockLauncher)) > 0 {
			return errors.New("adb 192.0.2.1:5555: device offline")
		}
		return nil
	}}
	if _, err := restore(t, f, o); err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "verify: lost after restoring the stock: ") {
		t.Fatalf("err = %v", err)
	}
}

// the platform daemons the hook stopped while v2 ran (midi's bluetooth player and agent) start again with the stock,
// after it; one that will not start is said, and the stock is restored all the same
func TestRestoreStockStartsThePlatformDaemons(t *testing.T) {
	f := midiSpeaker(t, true)
	f.p.PlatformDaemons = []Daemon{{Exe: "/usr/bin/bluealsa-aplay", Args: []string{"-M", "disable", "-S"}}, {Exe: "/usr/bin/agent"}}
	mustInstallMidi(t, f)
	f.ops = nil
	f.fail = map[string]error{"start-stop-daemon -x /usr/bin/agent": errors.New("unable to stat")}
	out, err := restore(t, f, restoreOpts(t, f))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ops := f.changes()
	stock := slices.Index(ops, "start-stop-daemon "+stockStart)
	player := slices.Index(ops, "start-stop-daemon -x /usr/bin/bluealsa-aplay -M disable -S")
	if stock < 0 || player < stock || !strings.Contains(out, "/usr/bin/agent did not start") {
		t.Fatalf("commands:\n%s\noutput:\n%s", strings.Join(ops, "\n"), out)
	}
}
