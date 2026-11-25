package install

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	adbd     = "::respawn:/usr/bin/adbd"
	dropbear = "::respawn:/usr/sbin/dropbear -F -p 22 -r /etc/dropbear/dropbear_ed25519_host_key"
	v1Line   = "::respawn:/data/cadence/cadenced.init"
	v2Line   = "::respawn:/data/cadence/bin/cadence-run"
)

// original is a provisioned v1 speaker's inittab, with lines that look like v1's but are not; stopped is it without
// v1's line, and swapped with v2's where v1's was
var (
	tabHead  = []string{"# /etc/inittab", "::sysinit:/etc/init.d/rcS", adbd}
	tabTail  = []string{dropbear, "#" + v1Line, "::once:/data/cadence/cadenced.init --check", v1Line + "d", "ttyS0::respawn:/sbin/getty -L ttyS0 115200 vt100", "::shutdown:/etc/init.d/rcK"}
	original = strings.Join(slices.Concat(tabHead, []string{v1Line}, tabTail), "\n") + "\n"
	stopped  = strings.Join(slices.Concat(tabHead, tabTail), "\n") + "\n"
	swapped  = strings.Join(slices.Concat(tabHead, []string{v2Line}, tabTail), "\n") + "\n"
)

func testProfile() Profile {
	return Profile{
		Board: "yandex-pickle", Arch: "arm64", DataRoot: "/data/cadence", DataMount: "/data", Inittab: "/etc/inittab",
		ModelPath: "/proc/device-tree/model", ModelPrefix: "Test Board",
		KernelPath: "/proc/sys/kernel/osrelease", KernelFamily: "5.4.",
		Facts: []Fact{
			{Name: "kernel", Paths: []string{"/proc/sys/kernel/osrelease"}, Want: "5.4.242"},
			{Name: "VERSION", Paths: []string{"/usr/lib/stock/VERSION", "/vendor/assistant/VERSION"}, Want: "3.21"},
		},
		Recovery: []string{adbd, dropbear}, RecoveryDaemons: []string{"adbd", "dropbear"}, Anchor: adbd,
		Vendor: []string{"/vendor/assistant", "/usr/bin/vendor-ota"}, VendorDaemons: []string{"maind"},
		InittabForbidden: []string{"/vendor/"}, MixerControl: "Master", Tools: []string{"tar"}, MinRootKB: 2048,
	}
}

// v1Speaker is a provisioned pickle running v1, its level 45, with the owner's command model in stock/
func v1Speaker(t *testing.T) *fake { return v1SpeakerOn(t, testProfile()) }

func v1SpeakerOn(t *testing.T, p Profile) *fake {
	f := newFake(t, p)
	f.put("/proc/device-tree/model", []byte("Test Board rev 3\x00"), 0o444)
	f.put("/proc/sys/kernel/osrelease", []byte("5.4.242\n"), 0o444)
	f.put("/usr/lib/stock/VERSION", []byte("3.21\n"), 0o644)
	f.put("/etc/inittab", []byte(original), 0o644)
	for p, data := range map[string]string{
		"cadenced": "elf", "cadenced.init": "#!/bin/sh", "rollback.sh": "#!/bin/sh", "ca-certificates.crt": "pem",
		"board-control": "#!/bin/sh", "board.txt": "pickle\n", "stock-mode": "removed\n", "cadence-media": "elf",
		"config.json": `{"deviceToken":"v1-secret-token"}`, "log/cadenced.log": "v1 log\n", "state/volume": "45\n",
		"state/settings": "{}", "soloist/runtime/lib/ld.so": "elf", "soloist/soloist": "elf", "soloist.key": "key",
		"airplay/bin/shairport-sync": "elf", "stock/command/model.bin": "the owner's model",
	} {
		f.put("/data/cadence/"+p, []byte(data), 0o600)
	}
	f.mkdirAll("/data/cadence/run")
	f.boot()
	return f
}

type bundleFile struct {
	name string
	data string
	kind byte
}

func bundleFiles() []bundleFile {
	return []bundleFile{
		{name: "bin/", kind: tar.TypeDir}, {name: "bin/cadenced", data: "v2 cadenced"}, {name: "bin/cadence-audiod", data: "audiod"},
		{name: "bin/lib/", kind: tar.TypeDir}, {name: "bin/lib/libavutil.so.61", data: "ffmpeg"},
		{name: "share/", kind: tar.TypeDir}, {name: "share/ca-certificates.crt", data: "pem"},
		{name: "share/cadence-run", data: "#!/bin/sh\n# the wrapper\n"}, {name: "share/board-hook", data: "# the board's hook\n"},
		{name: "licenses/", kind: tar.TypeDir}, {name: "licenses/README", data: "notices"},
		{name: "soloist/", kind: tar.TypeDir}, {name: "soloist/runtime/", kind: tar.TypeDir},
		{name: "soloist/runtime/lib/", kind: tar.TypeDir}, {name: "soloist/runtime/lib/ld-linux-aarch64.so.1", data: "loader"},
		{name: "airplay/", kind: tar.TypeDir}, {name: "airplay/runtime/", kind: tar.TypeDir},
		{name: "airplay/runtime/bin/", kind: tar.TypeDir}, {name: "airplay/runtime/bin/shairport-sync", data: "shairport"},
	}
}

var bundleHeaders = []string{"cadence 72407e22cafd", "board yandex-pickle", "arch arm64", "go go1.27.3"}

// writeBundle writes a tarball as pack-device.sh does: MANIFEST first, then the tree
func writeBundle(t *testing.T, files []bundleFile, headers []string, manifest func(string) string) string {
	t.Helper()
	var m strings.Builder
	for _, h := range headers {
		m.WriteString("# " + h + "\n")
	}
	for _, f := range files {
		if f.kind == 0 {
			s := sha256.Sum256([]byte(f.data))
			m.WriteString(hex.EncodeToString(s[:]) + "  " + f.name + "\n")
		}
	}
	text := m.String()
	if manifest != nil {
		text = manifest(text)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name, data string, kind byte) {
		mode := int64(0o644)
		if kind == tar.TypeDir || strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		h := &tar.Header{Name: name, Typeflag: kind, Mode: mode, Size: int64(len(data)), Format: tar.FormatGNU}
		if kind == tar.TypeSymlink {
			h.Linkname, h.Size = data, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write([]byte(data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("MANIFEST", text, tar.TypeReg)
	for _, f := range files {
		kind := f.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		add(f.name, f.data, kind)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "cadence-72407e22cafd-yandex-pickle-arm64.tar")
	if err := os.WriteFile(file, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func testBundle(t *testing.T) *Bundle {
	t.Helper()
	b, err := OpenBundle(writeBundle(t, bundleFiles(), bundleHeaders, nil), "yandex-pickle", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var at = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func runOpts(t *testing.T, f *fake) Options {
	return Options{
		Bundle: testBundle(t), Server: "https://cadence.example", Code: f.code, Now: func() time.Time { return at },
		Sleep: func(time.Duration) {}, LinkWait: 10 * time.Second,
	}
}

func install(t *testing.T, f *fake, o Options) (string, error) {
	t.Helper()
	var out bytes.Buffer
	o.Out = &out
	err := New(f, f.p, o).Install(context.Background())
	return out.String(), err
}

func rollback(t *testing.T, f *fake, o Options) (string, error) {
	t.Helper()
	var out bytes.Buffer
	o.Out = &out
	err := New(f, f.p, o).ToV1(context.Background())
	return out.String(), err
}

func mustInstall(t *testing.T, f *fake) string {
	t.Helper()
	out, err := install(t, f, runOpts(t, f))
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	return out
}

func TestInstallSwapsV1ForV2(t *testing.T) {
	f := v1Speaker(t)
	supervisor, daemon := f.pidsOf("/bin/busybox")[0], f.pidsOf("/data/cadence/cadenced")[0]
	out := mustInstall(t, f)

	if got := string(f.get("/etc/inittab")); got != swapped {
		t.Fatalf("inittab =\n%s\nwant\n%s", got, swapped)
	}
	if f.rootRW {
		t.Fatal("the root was left writable")
	}
	if slices.Contains(f.loaded, v1Line) || !slices.Contains(f.loaded, v2Line) {
		t.Fatalf("init's table = %q", f.loaded)
	}
	if len(f.pidsOf("/data/cadence/cadenced")) != 0 || len(f.pidsOf("/data/cadence/bin/cadenced")) != 1 {
		t.Fatalf("processes: %+v", f.procs)
	}
	for p, want := range map[string]string{
		"/data/cadence/v1/cadenced": "elf", "/data/cadence/v1/config.json": `{"deviceToken":"v1-secret-token"}`,
		"/data/cadence/v1/state/volume": "45\n", "/data/cadence/v1/soloist/soloist": "elf",
		"/data/cadence/stock/command/model.bin": "the owner's model",
		"/data/cadence/state/volume":            `{"level":45}`,
		"/data/cadence/bin/cadenced":            "v2 cadenced", "/data/cadence/bin/cadence-run": "#!/bin/sh\n# the wrapper\n",
		"/data/cadence/share/cadence-run": "#!/bin/sh\n# the wrapper\n", "/data/cadence/licenses/README": "notices",
		"/data/cadence/bin/board-hook": "# the board's hook\n", "/data/cadence/share/board-hook": "# the board's hook\n",
		"/data/cadence/soloist/runtime/lib/ld-linux-aarch64.so.1": "loader",
		"/data/cadence/airplay/runtime/bin/shairport-sync":        "shairport",
		"/data/cadence/install.state":                             "stop-v1\nv1-moved\nstage\nenroll\nstart-v2\nverify\n",
		"/data/cadence/backup/inittab.20261109T120000Z.stop-v1":   original,
		"/data/cadence/backup/inittab.20261109T120000Z.start-v2":  stopped,
	} {
		if got := string(f.get(p)); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	if f.files["/data/cadence/bin/cadence-run"].mode != 0o755 || f.files["/data/cadence/bin/board-hook"].mode != 0o644 ||
		f.files["/data/cadence/state/volume"].mode != 0o600 || f.files["/data/cadence/backup"].mode != 0o700 {
		t.Error("modes are not 755 for the wrapper, 644 for its hook, 600 for the level and 700 for the backups")
	}
	for _, gone := range []string{"/data/cadence/.install", "/data/cadence/cadenced", "/data/cadence/soloist/soloist", "/data/cadence/soloist.key"} {
		if f.has(gone) {
			t.Errorf("%s is still there", gone)
		}
	}
	if !strings.Contains(string(f.get("/data/cadence/config.json")), f.token) {
		t.Error("enroll wrote no config")
	}

	// each phase's commands, in order: v1 leaves init before it is stopped, the root is writable only around the
	// inittab writes, and v2's line goes in only once its tree is in place
	want := []string{
		"mkdir /data/cadence/.install 700",
		// stop-v1
		"write /data/cadence/.install/board-hook 644",
		"write /data/cadence/.install/cadence-run 755",
		"mkdir /data/cadence/backup 700",
		"cp /etc/inittab /data/cadence/backup/inittab.20261109T120000Z.stop-v1 600",
		"write /data/cadence/.install/inittab.next 600",
		"swap-inittab /data/cadence/.install/inittab.next", "remount rw", "write-inittab", "hup", "remount ro",
		fmt.Sprintf("kill -TERM %d", supervisor), fmt.Sprintf("kill -TERM %d", daemon),
		"blank /data/cadence/.install/cadence-run",
		"write /data/cadence/install.state 600",
		// stage
		"mkdir /data/cadence/v1 700",
	}
	for _, n := range []string{"airplay", "board-control", "board.txt", "ca-certificates.crt", "cadence-media", "cadenced",
		"cadenced.init", "config.json", "log", "rollback.sh", "run", "soloist", "soloist.key", "state", "stock-mode"} {
		want = append(want, "mv /data/cadence/"+n+" /data/cadence/v1/"+n)
	}
	want = append(want,
		"write /data/cadence/install.state 600",
		"mkdir /data/cadence/state 700", "write /data/cadence/state/volume 600",
		"push /data/cadence/.install/cadence.tar",
		"rm /data/cadence/.install/tree",
		"unpack /data/cadence/.install/cadence.tar /data/cadence/.install/tree",
		"check /data/cadence/.install/tree",
	)
	for _, top := range []string{"MANIFEST", "bin", "share", "licenses", "soloist", "airplay"} {
		from, to := "/data/cadence/.install/tree/"+top, "/data/cadence/"+top
		if top == "soloist" || top == "airplay" {
			want = append(want, "mkdir "+to+" 700")
			from, to = from+"/runtime", to+"/runtime"
		}
		want = append(want, "rm "+to+".old", "mv "+from+" "+to, "rm "+to+".old")
	}
	want = append(want,
		"rm /data/cadence/.install/tree", "rm /data/cadence/.install/cadence.tar",
		"write /data/cadence/install.state 600",
		// enroll
		"write-secret /data/cadence/.install/code",
		"enroll https://cadence.example /data/cadence/.install/code /data/cadence/config.json",
		"rm /data/cadence/.install/code",
		"write /data/cadence/install.state 600",
		// start-v2
		"cp /data/cadence/share/board-hook /data/cadence/bin/board-hook 644",
		"cp /data/cadence/share/cadence-run /data/cadence/bin/cadence-run 755",
		"mkdir /data/cadence/backup 700",
		"cp /etc/inittab /data/cadence/backup/inittab.20261109T120000Z.start-v2 600",
		"write /data/cadence/.install/inittab.next 600",
		"swap-inittab /data/cadence/.install/inittab.next", "remount rw", "write-inittab", "hup", "remount ro",
		"write /data/cadence/install.state 600",
		// verify
		"write /data/cadence/install.state 600",
		"rm /data/cadence/.install",
	)
	if got := f.changes(); !slices.Equal(got, want) {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(out, "verify: recovery access kept, cadenced linked up, Master at 45 %") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestRerunSkipsWhatIsDone(t *testing.T) {
	f := v1Speaker(t)
	mustInstall(t, f)
	f.ops = nil
	out := mustInstall(t, f)
	want := []string{"mkdir /data/cadence/.install 700", "write /data/cadence/install.state 600", "rm /data/cadence/.install"}
	if got := f.changes(); !slices.Equal(got, want) {
		t.Fatalf("a second run changed:\n%s", strings.Join(got, "\n"))
	}
	if strings.Count(out, "done already") != len(New(f, f.p, Options{}).phases()) {
		t.Fatalf("output:\n%s", out)
	}
	// no code needed once enrolled
	f.ops = nil
	o := runOpts(t, f)
	o.Code = ""
	if out, err := install(t, f, o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// the room the tree needs is checked only while stage is to run: a re-run after it is never refused for a full /data
func TestRerunAfterStageNeedsNoRoom(t *testing.T) {
	f := v1Speaker(t)
	mustInstall(t, f)
	f.free["/data"] = 1000
	if out, err := install(t, f, runOpts(t, f)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// regression: pickle's /data, 147728 KiB free once v1's media went, refused the 47.7 MB tarball of 0c52aca6 for
// 158676 KiB. stage removes the tarball before soloist's first download, so the room is the tarball and the larger
// of the tree and the spare, never all three
func TestRoomCountsTheTarballOrTheSpare(t *testing.T) {
	f := v1Speaker(t)
	o := runOpts(t, f)
	o.Bundle.Size = 47687680
	need := o.Bundle.Size/1024 + 64<<10
	f.free["/data"] = need - 1
	if _, err := install(t, f, o); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), fmt.Sprintf("%d needed", need)) {
		t.Fatalf("err = %v", err)
	}
	f.free["/data"] = 147728
	if out, err := install(t, f, o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// a write that fails mid-way leaves inittab as it was, the root read-only, and v1 running: it is stopped only once
// init can no longer restart it
func TestFailedInittabWriteLeavesV1Running(t *testing.T) {
	f := v1Speaker(t)
	f.fail["write-inittab"] = errors.New("no space left on device")
	if _, err := install(t, f, runOpts(t, f)); err == nil || !strings.Contains(err.Error(), "stop-v1") {
		t.Fatalf("err = %v", err)
	}
	if string(f.get("/etc/inittab")) != original || f.rootRW {
		t.Fatal("inittab changed or the root is still writable")
	}
	ops := f.changes()
	if i := slices.Index(ops, "remount rw"); i < 0 || ops[len(ops)-1] != "remount ro" {
		t.Fatalf("commands: %q", ops)
	}
	if slices.ContainsFunc(ops, func(o string) bool { return strings.HasPrefix(o, "kill") }) || len(f.pidsOf("/data/cadence/cadenced")) != 1 {
		t.Fatal("v1 was stopped although its line stayed")
	}
}

func TestPidfileNamingAnotherProcessIsLeftAlone(t *testing.T) {
	f := v1Speaker(t)
	getty := f.pidsOf("/sbin/getty")[0]
	f.put("/data/cadence/run/cadenced.pid", []byte(fmt.Sprintf("%d\n", getty)), 0o644)
	out := mustInstall(t, f)
	for _, op := range f.changes() {
		if strings.HasPrefix(op, "kill") && strings.HasSuffix(op, fmt.Sprintf(" %d", getty)) {
			t.Fatalf("signalled the pid file's process: %s", op)
		}
	}
	if len(f.pidsOf("/sbin/getty")) != 1 || len(f.pidsOf("/data/cadence/cadenced")) != 0 {
		t.Fatal("getty stopped, or v1 kept running")
	}
	if !strings.Contains(out, fmt.Sprintf("names pid %d, which is not a cadence process: not signalled", getty)) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestStubbornV1IsKilled(t *testing.T) {
	f := v1Speaker(t)
	f.stubborn["/bin/busybox"], f.stubborn["/data/cadence/cadenced"] = true, true
	mustInstall(t, f)
	if !slices.ContainsFunc(f.changes(), func(o string) bool { return strings.HasPrefix(o, "kill -KILL") }) {
		t.Fatal("no KILL after TERM was ignored")
	}
}

func TestV1ThatWillNotDieStopsTheSwap(t *testing.T) {
	f := v1Speaker(t)
	f.ghost["/data/cadence/cadenced"] = true
	_, err := install(t, f, runOpts(t, f))
	if err == nil || !strings.Contains(err.Error(), "still running after KILL: v1's cadenced") {
		t.Fatalf("err = %v", err)
	}
	if f.has("/data/cadence/v1") || !f.has("/data/cadence/cadenced") {
		t.Fatal("v1's tree moved while v1 ran")
	}
}

// refusals happen in preflight, before anything changes
func TestPreflightRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*fake, *Options)
		want  string
	}{
		"vendor assistant": {func(f *fake, _ *Options) { f.put("/vendor/assistant/maind", nil, 0o755) }, "/vendor/assistant is present"},
		"vendor updater":   {func(f *fake, _ *Options) { f.put("/usr/bin/vendor-ota", nil, 0o755) }, "/usr/bin/vendor-ota is present"},
		"vendor daemon": {func(f *fake, _ *Options) {
			f.spawn(Proc{Exe: "/opt/x/maind", Args: []string{"maind"}}, "", 0)
		}, "maind is running"},
		"vendor in inittab": {func(f *fake, _ *Options) {
			f.put("/etc/inittab", []byte(original+"::respawn:/vendor/assistant/run.sh\n"), 0o644)
		}, "runs something under /vendor/"},
		"no dropbear entry": {func(f *fake, _ *Options) {
			f.put("/etc/inittab", withoutEntry([]byte(original), dropbear), 0o644)
		}, "has no " + dropbear + " entry"},
		"adbd not running": {func(f *fake, _ *Options) {
			for _, p := range f.pidsOf("/usr/bin/adbd") {
				delete(f.procs, p)
			}
		}, "adbd is not running"},
		"transport lost": {func(_ *fake, o *Options) {
			o.Recovery = []func(context.Context) error{func(context.Context) error { return errors.New("adb: device offline") }}
		}, "recovery access: adb: device offline"},
		"other board":    {func(f *fake, _ *Options) { f.put("/proc/device-tree/model", []byte("Other\x00"), 0o444) }, "not a yandex-pickle board"},
		"other kernel":   {func(f *fake, _ *Options) { f.put("/proc/sys/kernel/osrelease", []byte("4.9.68\n"), 0o444) }, `kernel "4.9.68"`},
		"firmware":       {func(f *fake, _ *Options) { f.put("/usr/lib/stock/VERSION", []byte("3.22\n"), 0o644) }, "firmware differs from the tested one (VERSION)"},
		"tools":          {func(f *fake, _ *Options) { f.missing = []string{"tar"} }, "the device lacks tar"},
		"space on /data": {func(f *fake, _ *Options) { f.free["/data"] = 1000 }, "/data has 1000 KiB free"},
		"space on /":     {func(f *fake, _ *Options) { f.free["/"] = 100 }, "/ has 100 KiB free, 2048 needed"},
		"no code":        {func(_ *fake, o *Options) { o.Code = "" }, "--code (from the dashboard) is required"},
		"http server":    {func(_ *fake, o *Options) { o.Server = "http://cadence.example" }, "--server must be"},
	} {
		t.Run(name, func(t *testing.T) {
			f := v1Speaker(t)
			o := runOpts(t, f)
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

func TestAcceptedFirmwareProceeds(t *testing.T) {
	f := v1Speaker(t)
	f.put("/usr/lib/stock/VERSION", []byte("3.22\n"), 0o644)
	o := runOpts(t, f)
	o.AcceptFirmware = true
	out, err := install(t, f, o)
	if err != nil || !strings.Contains(out, "firmware differs (VERSION): accepted") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// ruling M2-R9 as amended: a v1 file that cannot be read gives no v2 file, never a written 30, so v2 starts at 30 with
// the level unknown and no sender's volume counts against it (rule 7)
func TestVolumeIsConvertedOnce(t *testing.T) {
	for v1, want := range map[string]string{"45\n": `{"level":45}`, "150\n": "", "9999999\n": "", "garbage": "", "": ""} {
		f := v1Speaker(t)
		if v1 == "" {
			delete(f.files, "/data/cadence/state/volume")
		} else {
			f.put("/data/cadence/state/volume", []byte(v1), 0o600)
		}
		out := mustInstall(t, f)
		if want == "" {
			if f.has("/data/cadence/state/volume") {
				t.Errorf("v1 %q became %s, want no file", v1, f.get("/data/cadence/state/volume"))
			}
			if !strings.Contains(out, "no volume: v1's could not be read") {
				t.Errorf("v1 %q:\n%s", v1, out)
			}
		} else if got := string(f.get("/data/cadence/state/volume")); got != want {
			t.Errorf("v1 %q became %s, want %s", v1, got, want)
		}
		if wantMaster := map[bool]int{true: 30, false: 45}[want == ""]; f.mixer[0] != wantMaster {
			t.Errorf("v1 %q: Master at %d, want %d", v1, f.mixer[0], wantMaster)
		}
		if strings.Contains(string(f.get("/data/cadence/install.state")), "volume") {
			t.Errorf("v1 %q: install.state %q", v1, f.get("/data/cadence/install.state"))
		}
	}
}

func TestTarballThatArrivesDifferentIsRefused(t *testing.T) {
	f := v1Speaker(t)
	f.pushed = func(b []byte) []byte { return append(b, 0) }
	_, err := install(t, f, runOpts(t, f))
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "the tarball's sha256 on the device differs") {
		t.Fatalf("err = %v", err)
	}
	ops := f.changes()
	if ops[len(ops)-1] != "rm /data/cadence/.install/cadence.tar" || slices.ContainsFunc(ops, func(o string) bool { return strings.HasPrefix(o, "unpack") }) {
		t.Fatalf("commands: %q", ops)
	}
}

// a resumed run pushes nothing it has
func TestResumeAfterRefusedCode(t *testing.T) {
	f := v1Speaker(t)
	o := runOpts(t, f)
	o.Code = "WRONG-CODE"
	out, err := install(t, f, o)
	if !errors.Is(err, ErrEnrollRefused) || strings.Contains(out, "WRONG-CODE") || strings.Contains(err.Error(), "WRONG-CODE") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if f.has("/data/cadence/.install/code") {
		t.Fatal("the code stayed on the device")
	}
	f.ops = nil
	mustInstall(t, f)
	ops := f.changes()
	if ops[1] != "write-secret /data/cadence/.install/code" {
		t.Fatalf("the resumed run did not start at enroll: %q", ops)
	}
}

func TestVerifyNeedsLinkUpAndTheLevel(t *testing.T) {
	f := v1Speaker(t)
	f.linkUp = false
	if _, err := install(t, f, runOpts(t, f)); err == nil || !strings.Contains(err.Error(), "verify: no link_up") {
		t.Fatalf("err = %v", err)
	}
	f.linkUp = true
	f.appendLog(`{"msg":"link_up"}`)
	f.mixer = []int{45, 100}
	if _, err := install(t, f, runOpts(t, f)); err == nil || !strings.Contains(err.Error(), "Master is at 100 %, the persisted level is 45") {
		t.Fatalf("err = %v", err)
	}
	f.mixer = []int{44, 46}
	mustInstall(t, f)
}

func TestToV1RestoresV1(t *testing.T) {
	f := v1Speaker(t)
	mustInstall(t, f)
	// the owner turned it down under v2
	f.put("/data/cadence/state/volume", []byte(`{"level":20}`), 0o600)
	f.ops = nil
	o := runOpts(t, f)
	o.Bundle = nil
	out, err := rollback(t, f, o)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := string(f.get("/etc/inittab")); got != original {
		t.Fatalf("inittab =\n%s\nwant the original byte for byte", got)
	}
	if f.rootRW || !slices.Contains(f.loaded, v1Line) || slices.Contains(f.loaded, v2Line) {
		t.Fatalf("root writable %v, init's table %q", f.rootRW, f.loaded)
	}
	if len(f.pidsOf("/data/cadence/cadenced")) != 1 || len(f.pidsOf("/data/cadence/bin/cadenced")) != 0 {
		t.Fatalf("processes: %+v", f.procs)
	}
	for p, want := range map[string]string{
		"/data/cadence/cadenced": "elf", "/data/cadence/config.json": `{"deviceToken":"v1-secret-token"}`,
		"/data/cadence/state/volume": "20\n", "/data/cadence/v2/bin/cadenced": "v2 cadenced",
		"/data/cadence/v2/state/volume": `{"level":20}`, "/data/cadence/stock/command/model.bin": "the owner's model",
	} {
		if got := string(f.get(p)); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	for _, gone := range []string{"/data/cadence/v1", "/data/cadence/bin", "/data/cadence/install.state", "/data/cadence/.install"} {
		if f.has(gone) {
			t.Errorf("%s is still there", gone)
		}
	}
	// v2 leaves init before it is stopped; the lights go off with it
	ops := f.changes()
	swap, term := slices.Index(ops, "swap-inittab /data/cadence/.install/inittab.next"), slices.IndexFunc(ops, func(o string) bool { return strings.HasPrefix(o, "kill -TERM") })
	if swap < 0 || term < swap || !slices.Contains(ops, "blank /data/cadence/bin/cadence-run") {
		t.Fatalf("commands:\n%s", strings.Join(ops, "\n"))
	}
	// once more: nothing to do
	f.ops = nil
	if out, err := rollback(t, f, o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, op := range f.changes() {
		if strings.HasPrefix(op, "mv") || strings.HasPrefix(op, "swap") || strings.HasPrefix(op, "kill") {
			t.Fatalf("a second run changed: %q", f.changes())
		}
	}
	// and v2 again, with a new code
	f.ops = nil
	mustInstall(t, f)
	if got := string(f.get("/etc/inittab")); got != swapped {
		t.Fatalf("inittab after a second install =\n%s", got)
	}
}

// an inittab where v1's line is not where v1's install.sh puts it, and the last line has no newline, comes back as
// v2 keeps no level while v1's file was unreadable and the owner set none under v2: v1 then keeps its own; once the
// owner set one under v2, v1 gets it
func TestToV1KeepsV1sLevelUnlessV2HasOne(t *testing.T) {
	for moved, want := range map[bool]string{false: "garbage", true: "20\n"} {
		f := v1Speaker(t)
		f.put("/data/cadence/state/volume", []byte("garbage"), 0o600)
		mustInstall(t, f)
		if moved {
			f.put("/data/cadence/state/volume", []byte(`{"level":20}`), 0o600)
		}
		o := runOpts(t, f)
		o.Bundle = nil
		out, err := rollback(t, f, o)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if got := string(f.get("/data/cadence/state/volume")); got != want {
			t.Errorf("moved %v: v1's level %q, want %q\n%s", moved, got, want, out)
		}
	}
}

// it was: from the backup, not rebuilt
func TestToV1RestoresTheBackupByteForByte(t *testing.T) {
	f := v1Speaker(t)
	odd := "::sysinit:/etc/init.d/rcS\r\n" + adbd + "\n" + dropbear + "\n" + v1Line + "\n::shutdown:/etc/init.d/rcK"
	f.put("/etc/inittab", []byte(odd), 0o644)
	mustInstall(t, f)
	o := runOpts(t, f)
	o.Bundle = nil
	if out, err := rollback(t, f, o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := string(f.get("/etc/inittab")); got != odd {
		t.Fatalf("inittab = %q, want %q", got, odd)
	}
}

func TestToV1WithoutBackupPutsV1sLineBack(t *testing.T) {
	f := v1Speaker(t)
	mustInstall(t, f)
	for _, k := range f.under("/data/cadence/backup") {
		delete(f.files, k)
	}
	o := runOpts(t, f)
	o.Bundle = nil
	if out, err := rollback(t, f, o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := string(f.get("/etc/inittab")); got != original {
		t.Fatalf("inittab =\n%s", got)
	}
}

// a rollback cut short while v1's tree moves back resumes without mixing the trees
func TestToV1Resumes(t *testing.T) {
	f := v1Speaker(t)
	mustInstall(t, f)
	o := runOpts(t, f)
	o.Bundle = nil
	f.fail["mv /data/cadence/v1/state "] = errors.New("cut short")
	if _, err := rollback(t, f, o); err == nil {
		t.Fatal("no error")
	}
	delete(f.fail, "mv /data/cadence/v1/state ")
	if out, err := rollback(t, f, o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if string(f.get("/data/cadence/state/volume")) != "45\n" || string(f.get("/data/cadence/v2/state/volume")) != `{"level":45}` ||
		!f.has("/data/cadence/cadenced") || f.has("/data/cadence/v2/cadenced") {
		t.Fatal("the trees got mixed")
	}
	if string(f.get("/etc/inittab")) != original {
		t.Fatal("inittab not restored")
	}
}

func TestToV1RefusesWithoutV1(t *testing.T) {
	f := v1Speaker(t)
	delete(f.files, "/data/cadence/cadenced.init")
	o := runOpts(t, f)
	o.Bundle = nil
	if _, err := rollback(t, f, o); !errors.Is(err, ErrRefused) || len(f.changes()) != 0 {
		t.Fatalf("err = %v, changes %q", err, f.changes())
	}
}

// the code and the token never reach the output, whatever happens
// rule 2 on every inittab change: one that would drop a recovery entry, or any other line, is refused before
// anything is written
func TestInittabChangeThatLosesALineIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		entry  string
		change func([]byte) ([]byte, error)
		want   string
	}{
		"dropbear": {dropbear, func(cur []byte) ([]byte, error) { return withoutEntry(cur, dropbear), nil }, "would lose " + dropbear},
		"adbd":     {adbd, func(cur []byte) ([]byte, error) { return withoutEntry(cur, adbd), nil }, "would lose " + adbd},
		"another line": {v1Line, func(cur []byte) ([]byte, error) {
			return bytes.Replace(withoutEntry(cur, v1Line), []byte("::shutdown:/etc/init.d/rcK\n"), nil, 1), nil
		}, "would lose lines"},
	} {
		t.Run(name, func(t *testing.T) {
			f := v1Speaker(t)
			err := New(f, f.p, runOpts(t, f)).setInittab(context.Background(), "test", tc.entry, tc.change)
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(f.changes()) != 0 {
				t.Fatalf("err = %v, changes %q", err, f.changes())
			}
		})
	}
}

// rule 2 before every inittab write too, on both boards: recovery access lost since preflight (its entry or its
// daemon) fails the write with nothing written, a failure to act on (resume or --to-v1) rather than a refusal
func TestInittabWriteNeedsRecoveryFirst(t *testing.T) {
	for name, lose := range map[string]func(*fake){
		"entry": func(f *fake) { f.put("/etc/inittab", withoutEntry(f.get("/etc/inittab"), dropbear), 0o644) },
		"daemon": func(f *fake) {
			for _, p := range f.pidsOf("/usr/sbin/dropbear") {
				delete(f.procs, p)
			}
		},
	} {
		for board, speaker := range map[string]func(*testing.T) *fake{
			"pickle": v1Speaker, "midi": func(t *testing.T) *fake { return midiSpeaker(t, true) },
		} {
			t.Run(board+" "+name, func(t *testing.T) {
				f := speaker(t)
				lose(f)
				err := New(f, f.p, runOpts(t, f)).setInittab(context.Background(), "test", v1Line, func(cur []byte) ([]byte, error) {
					return withoutEntry(cur, v1Line), nil
				})
				if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "before rewriting /etc/inittab: ") ||
					!strings.Contains(err.Error(), "recovery access") || len(f.changes()) != 0 {
					t.Fatalf("err = %v, changes %q", err, f.changes())
				}
			})
		}
	}
}

// dropbearDies is a reload that takes dropbear down once init's table no longer has line
func dropbearDies(line string) func(*fake) {
	return func(f *fake) {
		if !slices.Contains(f.loaded, line) {
			for _, p := range f.pidsOf("/usr/sbin/dropbear") {
				delete(f.procs, p)
			}
		}
	}
}

// a reload that lost recovery access stops the run before v1 is stopped or a tree moves
func TestRecoveryLostAtAReloadStopsTheSwap(t *testing.T) {
	f := v1Speaker(t)
	f.reloaded = dropbearDies(v1Line)
	_, err := install(t, f, runOpts(t, f))
	if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "stop-v1: recovery access lost after rewriting /etc/inittab") {
		t.Fatalf("err = %v", err)
	}
	if slices.ContainsFunc(f.changes(), func(o string) bool { return strings.HasPrefix(o, "kill") || strings.HasPrefix(o, "mv") }) {
		t.Fatalf("went on after the reload: %q", f.changes())
	}
}

func TestRecoveryLostAtAReloadStopsTheRollback(t *testing.T) {
	f := v1Speaker(t)
	mustInstall(t, f)
	f.ops, f.reloaded = nil, dropbearDies(v2Line)
	o := runOpts(t, f)
	o.Bundle = nil
	_, err := rollback(t, f, o)
	if err == nil || !strings.Contains(err.Error(), "stop-v2: recovery access lost after rewriting /etc/inittab") {
		t.Fatalf("err = %v", err)
	}
	if slices.ContainsFunc(f.changes(), func(o string) bool { return strings.HasPrefix(o, "kill") || strings.HasPrefix(o, "mv") }) {
		t.Fatalf("went on after the reload: %q", f.changes())
	}
}

// access lost once the swap is through fails verify, as a failure to act on rather than a refusal
func TestRecoveryLostAfterTheSwapFailsVerify(t *testing.T) {
	f := v1Speaker(t)
	o := runOpts(t, f)
	o.Recovery = []func(context.Context) error{func(context.Context) error {
		if strings.Contains(string(f.get("/data/cadence/install.state")), "start-v2") {
			return errors.New("ssh root@192.0.2.1: Connection refused")
		}
		return nil
	}}
	_, err := install(t, f, o)
	if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "verify: lost after the swap: ") ||
		!strings.Contains(err.Error(), "Connection refused") {
		t.Fatalf("err = %v", err)
	}

	f = v1Speaker(t)
	mustInstall(t, f)
	o = runOpts(t, f)
	o.Bundle = nil
	o.Recovery = []func(context.Context) error{func(context.Context) error {
		if !f.has("/data/cadence/install.state") {
			return errors.New("adb 192.0.2.1:5555: device offline")
		}
		return nil
	}}
	if _, err := rollback(t, f, o); err == nil || !strings.Contains(err.Error(), "verify: lost after the swap: ") {
		t.Fatalf("rollback err = %v", err)
	}
}

func TestSecretsNeverInOutput(t *testing.T) {
	f := v1Speaker(t)
	f.code = "SECRET-CODE-1234"
	o := runOpts(t, f)
	o.Code = f.code
	var all strings.Builder
	out, err := install(t, f, o)
	all.WriteString(out)
	if err != nil {
		t.Fatal(err)
	}
	o.Code = "SECRET-WRONG-9876"
	f2 := v1Speaker(t)
	out, err = install(t, f2, o)
	all.WriteString(out + fmt.Sprint(err))
	for _, op := range append(f.changes(), f2.changes()...) {
		all.WriteString(op + "\n")
	}
	for _, s := range []string{"SECRET-CODE-1234", "SECRET-WRONG-9876", f.token, "v1-secret-token"} {
		if strings.Contains(all.String(), s) {
			t.Fatalf("%q reached the output", s)
		}
	}
}
