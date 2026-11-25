package install

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"
)

// a board that keeps its stock assistant (M2m-R2), with a neutral stock directory: its start script, launcher, an
// engine library and its version file
const (
	stockDir      = "/opt/stock"
	stockStart    = stockDir + "/run.sh"
	stockLauncher = stockDir + "/launcher"
	stockLibrary  = stockDir + "/libs/libengine.so"
)

func testMidiProfile() Profile {
	p := testProfile()
	p.Board, p.Arch = "yandex-midi", "armhf"
	p.Model, p.ModelPrefix = "Test Station", ""
	p.KernelFamily = "4.9."
	p.Facts = []Fact{
		{Name: "kernel", Paths: []string{"/proc/sys/kernel/osrelease"}, Want: "4.9.68"},
		{Name: "VERSION", Paths: []string{stockDir + "/VERSION"}, Want: "3.21"},
	}
	p.Vendor, p.VendorDaemons, p.InittabForbidden = nil, nil, []string{stockDir + "/"}
	p.Stock = &Stock{Dir: stockDir, Start: stockStart, Launcher: stockLauncher, Files: []string{stockLibrary}}
	p.RequireOTABlocked = true
	p.Tools = []string{"tar", "start-stop-daemon"}
	return p
}

// v1MidiTree is what v1's retained-stock install left in the data root, its own state file and board files included
var v1MidiTree = map[string]string{
	"cadenced": "elf32", "cadenced.init": "#!/bin/sh", "rollback.sh": "#!/bin/sh", "ca-certificates.crt": "pem",
	"board-control": "#!/bin/sh\n# suspend, resume\n", "board.txt": "yandexmidi\n", "stock-mode": "preserved\n",
	"provision.yandexmidi-preserved.state": "preflight\naccess\nsuspend\ncadence\nverify\n",
	"stock-audio-probe":                    "elf32", "cadence-media": "elf32", "runtime/lib/libssl.so.3": "elf32",
	"config.json": `{"deviceToken":"v1-secret-token"}`, "log/cadenced.log": "v1 log\n", "state/volume": "45\n",
	"soloist/runtime/lib/ld.so": "elf", "soloist.key": "key", "airplay/bin/shairport-sync": "elf32",
	"stock/command/model.bin": "the owner's model",
}

// midiSpeaker is a board that keeps its stock assistant, which its init script started at boot: with v1, whose
// supervisor suspended it at its start, or with only the recovery access v1's provisioning gave it
func midiSpeaker(t *testing.T, v1 bool) *fake {
	f := newFake(t, testMidiProfile())
	f.put("/proc/device-tree/model", []byte("Test Station\x00"), 0o444)
	f.put("/proc/sys/kernel/osrelease", []byte("4.9.68\n"), 0o444)
	f.put(stockDir+"/VERSION", []byte("3.21\n"), 0o644)
	f.put(stockStart, []byte("#!/bin/sh\n"), 0o755)
	f.put(stockLauncher, []byte("elf32"), 0o755)
	f.put(stockLibrary, []byte("elf32"), 0o644)
	f.startStock()
	tab := stopped
	if v1 {
		tab = original
		for p, data := range v1MidiTree {
			mode := fs.FileMode(0o600)
			if strings.HasSuffix(p, ".init") || strings.HasSuffix(p, ".sh") || p == "board-control" || p == "cadenced" {
				mode = 0o755
			}
			f.put("/data/cadence/"+p, []byte(data), mode)
		}
		f.mkdirAll("/data/cadence/run")
	}
	f.put("/etc/inittab", []byte(tab), 0o644)
	f.boot()
	return f
}

func midiBundle(t *testing.T) *Bundle {
	t.Helper()
	headers := []string{"cadence 72407e22cafd", "board yandex-midi", "arch armhf", "go go1.27.3"}
	b, err := OpenBundle(writeBundle(t, bundleFiles(), headers, nil), "yandex-midi", "armhf")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func midiOpts(t *testing.T, f *fake) Options {
	o := runOpts(t, f)
	o.Bundle, o.OTABlocked = midiBundle(t), true
	return o
}

func mustInstallMidi(t *testing.T, f *fake) string {
	t.Helper()
	out, err := install(t, f, midiOpts(t, f))
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	return out
}

// the ops between two recorded ones, both excluded
func opsBetween(t *testing.T, ops []string, after, before string) []string {
	t.Helper()
	i, j := slices.Index(ops, after), slices.Index(ops, before)
	if i < 0 || j < i {
		t.Fatalf("no %q before %q in:\n%s", after, before, strings.Join(ops, "\n"))
	}
	return ops[i+1 : j]
}

// before anything of v2 is staged, the stock is suspended by the wrapper's own hook from the work dir, as every start
// of cadenced will: with v1 (stock a watchdog brought back after v1 suspended it) and without
func TestMidiInstallSuspendsTheStockBeforeStaging(t *testing.T) {
	for name, v1 := range map[string]bool{"from v1": true, "without v1": false} {
		t.Run(name, func(t *testing.T) {
			f := midiSpeaker(t, v1)
			if v1 {
				f.startStock()
			}
			if len(f.stockPids()) == 0 {
				t.Fatal("the stock does not run before the install")
			}
			out := mustInstallMidi(t, f)
			ops := f.changes()
			suspend := opsBetween(t, ops, "blank /data/cadence/.install/cadence-run", "mkdir /data/cadence/v1 700")
			want := []string{
				"write /data/cadence/install.state 600",
				"write /data/cadence/.install/board-hook 644", "write /data/cadence/.install/cadence-run 755",
				"prestart /data/cadence/.install/cadence-run",
				"write /data/cadence/install.state 600",
			}
			if !slices.Equal(suspend, want) {
				t.Fatalf("between stop-v1 and stage:\n%s\nwant\n%s", strings.Join(suspend, "\n"), strings.Join(want, "\n"))
			}
			if got := string(f.get("/data/cadence/install.state")); got != "stop-v1\nsuspend\nv1-moved\nstage\nenroll\nstart-v2\nverify\n" {
				t.Fatalf("install.state %q", got)
			}
			wantTab := strings.Replace(stopped, adbd+"\n", adbd+"\n"+v2Line+"\n", 1)
			if got := string(f.get("/etc/inittab")); got != wantTab || f.rootRW {
				t.Fatalf("inittab =\n%s", got)
			}
			if len(f.stockPids()) != 0 || !f.has(stockDir) || len(f.pidsOf("/data/cadence/bin/cadenced")) != 1 {
				t.Fatalf("stock %v, v2 %v", f.stockPids(), f.pidsOf("/data/cadence/bin/cadenced"))
			}
			for _, s := range []string{"suspend: the stock assistant is suspended", "nothing runs from /opt/stock"} {
				if !strings.Contains(out, s) {
					t.Errorf("output lacks %q:\n%s", s, out)
				}
			}
			// v1's 45 becomes cadenced's own format, and the wrapper sets it (rule 7); without v1 there is no level
			wantVolume, wantMaster := "", []int{30, 30}
			if v1 {
				wantVolume, wantMaster = `{"level":45}`, []int{45, 45}
				for _, p := range []string{"provision.yandexmidi-preserved.state", "board-control", "stock-mode", "board.txt", "runtime/lib/libssl.so.3"} {
					if got := string(f.get("/data/cadence/v1/" + p)); got != v1MidiTree[p] || f.has("/data/cadence/"+p) {
						t.Errorf("%s: v1/ holds %q", p, got)
					}
				}
			}
			if got := string(f.get("/data/cadence/state/volume")); got != wantVolume || !slices.Equal(f.mixer, wantMaster) {
				t.Errorf("state/volume %q, Master %v", got, f.mixer)
			}
		})
	}
}

// a stock that will not stop stops the run at suspend: nothing staged, v1's tree in place, recovery access as it was;
// a later run resumes there
func TestMidiSuspendThatFailsStopsTheRun(t *testing.T) {
	f := midiSpeaker(t, true)
	f.ghost[stockDir+"/maind"] = true
	f.startStock()
	_, err := install(t, f, midiOpts(t, f))
	if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "suspend: ") ||
		!strings.Contains(err.Error(), "reason=stock_still_running") {
		t.Fatalf("err = %v", err)
	}
	if slices.ContainsFunc(f.changes(), func(o string) bool { return strings.HasPrefix(o, "mv ") || strings.HasPrefix(o, "push") }) {
		t.Fatalf("staged after a failed suspend: %q", f.changes())
	}
	tab := f.get("/etc/inittab")
	if !hasEntry(tab, adbd) || !hasEntry(tab, dropbear) || f.rootRW || len(f.pidsOf("/usr/bin/adbd")) != 1 ||
		len(f.pidsOf("/usr/sbin/dropbear")) != 1 {
		t.Fatal("recovery access changed")
	}
	if !f.has("/data/cadence/cadenced.init") || string(f.get("/data/cadence/install.state")) != "stop-v1\n" {
		t.Fatalf("v1's tree moved or install.state %q", f.get("/data/cadence/install.state"))
	}

	delete(f.ghost, stockDir+"/maind")
	f.ops = nil
	mustInstallMidi(t, f)
	if ops := f.changes(); ops[1] != "write /data/cadence/.install/board-hook 644" || ops[3] != "prestart /data/cadence/.install/cadence-run" {
		t.Fatalf("the resumed run did not start at suspend: %q", ops)
	}
}

func TestMidiRerunSkipsWhatIsDone(t *testing.T) {
	f := midiSpeaker(t, true)
	mustInstallMidi(t, f)
	f.ops = nil
	out := mustInstallMidi(t, f)
	want := []string{"mkdir /data/cadence/.install 700", "write /data/cadence/install.state 600", "rm /data/cadence/.install"}
	if got := f.changes(); !slices.Equal(got, want) {
		t.Fatalf("a second run changed:\n%s", strings.Join(got, "\n"))
	}
	if strings.Count(out, "done already") != 5 {
		t.Fatalf("output:\n%s", out)
	}
}

// a run cut short after suspend, then a reboot, whose init script starts the stock again: the resumed run suspends it
// again before the pending phase, whichever it is (suspend is idempotent)
func TestMidiResumedRunSuspendsAgain(t *testing.T) {
	for name, cut := range map[string]string{
		"stage": "push ", "enroll": "enroll ", "start-v2": "cp /data/cadence/share/board-hook ",
	} {
		t.Run(name, func(t *testing.T) {
			f := midiSpeaker(t, true)
			f.fail[cut] = errors.New("cut short")
			if _, err := install(t, f, midiOpts(t, f)); err == nil || !strings.HasPrefix(err.Error(), name+": ") {
				t.Fatalf("err = %v", err)
			}
			delete(f.fail, cut)
			f.procs = map[int]*fakeProc{}
			f.boot()
			f.startStock()
			stockAtCut := -1
			f.pushed = func(data []byte) []byte { stockAtCut = len(f.stockPids()); return data }
			f.ops = nil
			out := mustInstallMidi(t, f)
			ops := f.changes()
			prestart := slices.Index(ops, "prestart /data/cadence/.install/cadence-run")
			first := slices.IndexFunc(ops, func(o string) bool { return strings.HasPrefix(o, cut) })
			if prestart < 0 || first < prestart || !strings.Contains(out, "suspend: the stock assistant is suspended") ||
				strings.Contains(out, "suspend: done already") {
				t.Fatalf("the resumed run did not suspend before %s:\n%s\n%s", name, strings.Join(ops, "\n"), out)
			}
			if name == "stage" && stockAtCut != 0 {
				t.Fatalf("%d stock processes ran while the tarball was pushed", stockAtCut)
			}
		})
	}
}

// a stock that keeps coming back between the wrapper's polls (a shell loop the suspend leaves restarting it) fails
// verify a few polls on, once it was suspended again twice since cadenced started; once is a late start, and those
// before the latest start of cadenced do not count
func TestMidiVerifyFailsWhileTheStockKeepsReturning(t *testing.T) {
	for name, tc := range map[string]struct {
		comebacks int
		want      string
	}{
		"never":     {0, ""},
		"once":      {1, ""},
		"each poll": {1 << 10, "stock_keeps_returning: the wrapper suspended the stock again 3 times since cadenced started: run.sh"},
	} {
		t.Run(name, func(t *testing.T) {
			f := midiSpeaker(t, true)
			o := midiOpts(t, f)
			comebacks := tc.comebacks
			// time passes in the wrapper's polls of 2 s, each bringing the stock back while some are left
			o.Sleep = func(d time.Duration) {
				for range int(d / (2 * time.Second)) {
					if comebacks > 0 && slices.Contains(f.loaded, v2Line) {
						comebacks--
						f.appendLog("cadence-run: 2026-11-09T12:00:10 event=stock_resuspended count=1")
					}
				}
			}
			_, err := install(t, f, o)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || errors.Is(err, ErrRefused) || !strings.HasPrefix(err.Error(), "verify: ") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}

			// cadenced started again since: what came before does not count
			f.appendLog("cadence-run: 2026-11-09T12:01:00 event=start")
			f.appendLog(`{"time":"2026-11-09T12:01:01Z","level":"INFO","msg":"link_up"}`)
			comebacks = 1
			if _, err := install(t, f, o); err != nil {
				t.Fatalf("after a restart: %v", err)
			}
		})
	}
}

// --to-v1 puts v1's retained-stock tree back as it was, every file byte for byte and mode for mode (but the pid files
// its supervisor writes again when init starts it)
func TestMidiToV1RestoresItsTreeByteForByte(t *testing.T) {
	f := midiSpeaker(t, true)
	before := map[string]node{}
	for _, p := range f.under("/data/cadence") {
		if !f.files[p].dir && !slices.Contains(f.l.v1Pidfiles, p) {
			before[p] = *f.files[p]
		}
	}
	mustInstallMidi(t, f)
	o := midiOpts(t, f)
	o.Bundle = nil
	if out, err := rollback(t, f, o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := string(f.get("/etc/inittab")); got != original {
		t.Fatalf("inittab =\n%s", got)
	}
	for p, n := range before {
		got, ok := f.files[p]
		if !ok || got.dir != n.dir || got.mode != n.mode || string(got.data) != string(n.data) {
			t.Errorf("%s changed", p)
		}
	}
	for _, p := range f.under("/data/cadence") {
		if _, ok := before[p]; !ok && !f.files[p].dir && !slices.Contains(f.l.v1Pidfiles, p) &&
			!strings.HasPrefix(p, "/data/cadence/v2/") && !strings.HasPrefix(p, "/data/cadence/backup/") {
			t.Errorf("%s is new", p)
		}
	}
	if len(f.pidsOf("/data/cadence/cadenced")) != 1 || len(f.stockPids()) != 0 {
		t.Fatalf("v1 %v, stock %v", f.pidsOf("/data/cadence/cadenced"), f.stockPids())
	}
}

// verify fails while anything runs from the stock's directory, or once it is gone
func TestMidiVerifyNeedsTheStockSuspendedAndInstalled(t *testing.T) {
	for name, tc := range map[string]struct {
		reloaded func(*fake)
		want     string
	}{
		"stock back": {func(f *fake) {
			if slices.Contains(f.loaded, v2Line) && len(f.stockPids()) == 0 {
				f.spawn(Proc{Exe: stockDir + "/watchdog", Args: []string{"watchdog"}}, "", 0)
			}
		}, "runs from /opt/stock: the stock assistant is not suspended"},
		"stock gone": {func(f *fake) {
			for _, k := range f.under(stockDir) {
				delete(f.files, k)
			}
		}, "/opt/stock is gone"},
	} {
		t.Run(name, func(t *testing.T) {
			f := midiSpeaker(t, true)
			f.reloaded = tc.reloaded
			_, err := install(t, f, midiOpts(t, f))
			if err == nil || errors.Is(err, ErrRefused) || !strings.HasPrefix(err.Error(), "verify: ") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// every refusal comes before anything changes; a midi needs the owner's word that the router blocks OTA
func TestMidiPreflightRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*fake, *Options)
		want  string
	}{
		"ota not blocked": {func(_ *fake, o *Options) { o.OTABlocked = false }, "keep the router's rules that block the vendor's OTA servers"},
		"lookalike model": {func(f *fake, _ *Options) { f.put("/proc/device-tree/model", []byte("Test Station Pro\x00"), 0o444) }, "not a yandex-midi board"},
		"other kernel":    {func(f *fake, _ *Options) { f.put("/proc/sys/kernel/osrelease", []byte("5.4.242\n"), 0o444) }, `kernel "5.4.242" is not 4.9.x`},
		"firmware":        {func(f *fake, _ *Options) { f.put(stockDir+"/VERSION", []byte("3.22\n"), 0o644) }, "firmware differs from the tested one (VERSION)"},
		"no stock": {func(f *fake, _ *Options) {
			for _, k := range f.under(stockDir) {
				delete(f.files, k)
			}
		}, "/opt/stock is missing"},
		"start not executable": {func(f *fake, _ *Options) { f.files[stockStart].mode = 0o644 }, "/opt/stock/run.sh is not executable"},
		"no launcher":          {func(f *fake, _ *Options) { delete(f.files, stockLauncher) }, "/opt/stock/launcher is missing"},
		"no library":           {func(f *fake, _ *Options) { delete(f.files, stockLibrary) }, "/opt/stock/libs/libengine.so is missing"},
		"stock in inittab": {func(f *fake, _ *Options) {
			f.put("/etc/inittab", []byte(original+"::respawn:/opt/stock/run.sh\n"), 0o644)
		}, "runs something under /opt/stock/"},
		"no dropbear entry": {func(f *fake, _ *Options) {
			f.put("/etc/inittab", withoutEntry([]byte(original), dropbear), 0o644)
		}, "has no " + dropbear + " entry"},
		"tools":          {func(f *fake, _ *Options) { f.missing = []string{"start-stop-daemon"} }, "the device lacks start-stop-daemon"},
		"space on /data": {func(f *fake, _ *Options) { f.free["/data"] = 1000 }, "/data has 1000 KiB free"},
		"no code":        {func(_ *fake, o *Options) { o.Code = "" }, "--code (from the dashboard) is required"},
	} {
		t.Run(name, func(t *testing.T) {
			f := midiSpeaker(t, true)
			o := midiOpts(t, f)
			tc.setup(f, &o)
			_, err := install(t, f, o)
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(f.changes()) != 0 {
				t.Fatalf("a refused run changed: %q", f.changes())
			}
		})
	}
}

func TestMidiAcceptedFirmwareProceeds(t *testing.T) {
	f := midiSpeaker(t, true)
	f.put(stockDir+"/VERSION", []byte("3.22\n"), 0o644)
	o := midiOpts(t, f)
	o.AcceptFirmware = true
	if out, err := install(t, f, o); err != nil || !strings.Contains(out, "firmware differs (VERSION): accepted") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// the tree's room is the tarball plus the larger of it and 64 MiB, checked only while stage is to run
func TestMidiNeedsRoomOnlyWhileStaging(t *testing.T) {
	f := midiSpeaker(t, true)
	o := midiOpts(t, f)
	f.free["/data"] = 1000
	need := o.Bundle.Size/1024 + max(o.Bundle.Size/1024, 64<<10)
	if _, err := install(t, f, o); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), fmt.Sprintf("1000 KiB free, %d needed", need)) {
		t.Fatalf("err = %v", err)
	}
	f.free["/data"] = 1 << 20
	mustInstallMidi(t, f)
	f.free["/data"] = 1000
	mustInstallMidi(t, f)
}

// rule 2 after each of a midi's inittab writes: a reload that loses recovery access stops the run before anything
// else happens
func TestMidiReloadThatLosesRecoveryStopsTheRun(t *testing.T) {
	for name, tc := range map[string]struct {
		line, want string
		installed  bool
		restore    bool
		forbidden  []string
	}{
		"stop-v1":       {line: v1Line, want: "stop-v1: recovery access lost", forbidden: []string{"kill", "prestart", "mv "}},
		"start-v2":      {line: "", want: "start-v2: recovery access lost", forbidden: nil},
		"restore-stock": {line: v2Line, want: "stop-v2: recovery access lost", installed: true, restore: true, forbidden: []string{"kill", "start-stop-daemon"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := midiSpeaker(t, true)
			if tc.installed {
				mustInstallMidi(t, f)
				f.ops = nil
			}
			if tc.line != "" {
				f.reloaded = dropbearDies(tc.line)
			} else {
				f.reloaded = func(f *fake) {
					if slices.Contains(f.loaded, v2Line) {
						for _, p := range f.pidsOf("/usr/sbin/dropbear") {
							delete(f.procs, p)
						}
					}
				}
			}
			var err error
			if tc.restore {
				o := midiOpts(t, f)
				o.Bundle = nil
				_, err = restore(t, f, o)
			} else {
				_, err = install(t, f, midiOpts(t, f))
			}
			if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
			for _, op := range f.changes() {
				for _, bad := range tc.forbidden {
					if strings.HasPrefix(op, bad) {
						t.Fatalf("went on after the reload: %q", f.changes())
					}
				}
			}
		})
	}
}

// a midi install never applies its hardening: its steps are not inspected yet, and the router stands in (M2m-R3)
func TestMidiInstallNeverAppliesItsHardening(t *testing.T) {
	f := midiSpeaker(t, true)
	steps := []*spyStep{{name: "ota-key", state: StepNotInspected}, {name: "ota-script-stub", state: StepNotInspected},
		{name: "updatesd-launch", state: StepNotInspected}}
	for _, s := range steps {
		f.p.Hardening = append(f.p.Hardening, s)
	}
	out := mustInstallMidi(t, f)
	for _, s := range steps {
		if s.applied+s.reverted != 0 {
			t.Errorf("%s: applied %d, reverted %d", s.name, s.applied, s.reverted)
		}
		if !strings.Contains(out, "preflight: hardening "+s.name+": not_inspected") {
			t.Errorf("output lacks %s:\n%s", s.name, out)
		}
	}
	if !strings.Contains(out, "the router's OTA block stands in for them (--ota-blocked)") {
		t.Errorf("output:\n%s", out)
	}
	if f.has("/data/cadence/backup/ota-key") {
		t.Error("a step's backup dir was made")
	}
}

// the code, the tokens and v1's token never reach the output of a midi's install or restore
func TestMidiSecretsNeverInOutput(t *testing.T) {
	f := midiSpeaker(t, true)
	f.code = "SECRET-CODE-1234"
	o := midiOpts(t, f)
	o.Code = f.code
	var all strings.Builder
	out, err := install(t, f, o)
	all.WriteString(out + fmt.Sprint(err))
	o.Bundle = nil
	out, err = restore(t, f, o)
	all.WriteString(out + fmt.Sprint(err))
	for _, op := range f.changes() {
		all.WriteString(op + "\n")
	}
	for _, s := range []string{"SECRET-CODE-1234", f.token, "v1-secret-token"} {
		if strings.Contains(all.String(), s) {
			t.Fatalf("%q reached the output", s)
		}
	}
}

// --to-v1 starts the platform daemons again before v1
func TestMidiToV1StartsThePlatformDaemons(t *testing.T) {
	f := midiSpeaker(t, true)
	f.p.PlatformDaemons = []Daemon{{Exe: "/usr/bin/bluealsa-aplay", Args: []string{"-M", "disable", "-S"}}}
	mustInstallMidi(t, f)
	o := midiOpts(t, f)
	o.Bundle = nil
	f.ops = nil
	if out, err := rollback(t, f, o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !slices.Contains(f.changes(), "start-stop-daemon -x /usr/bin/bluealsa-aplay -M disable -S") {
		t.Fatalf("commands:\n%s", strings.Join(f.changes(), "\n"))
	}
}
