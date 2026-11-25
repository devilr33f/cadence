package isolation_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestCoreNamesNoHardware(t *testing.T) {
	banned := []string{"/dev/", "/sys/", "/proc/asound", "hw:"}
	scanned := 0
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, _ := strconv.Unquote(lit.Value)
			if strings.HasPrefix(v, "git.shino.boo/devilreef/cadence/device/boards") {
				t.Errorf("%s imports %s", path, v)
			}
			// the installer's commands redirect to /dev/null, which is no board's hardware
			v = strings.ReplaceAll(v, "/dev/null", "")
			for _, b := range banned {
				if strings.Contains(v, b) {
					t.Errorf("%s names hardware %q", path, b)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// a walk that silently misses the core would pass vacuously
	if scanned < 10 {
		t.Fatalf("scanned %d files, want at least 10", scanned)
	}
}

const (
	pickleBoard = "boards/yandex-pickle"
	midiBoard   = "boards/yandex-midi"
	microBoard  = "boards/yandex-micro"
)

var yandexBoards = []string{pickleBoard, midiBoard, microBoard}

// the pi reads its remotes through evdev and takes itself by its device tree, as the yandex boards do
const rpiBoard = "boards/rpi"

// the vk capsula's installer takes it by its device tree's compatible
const vkBoard = "boards/vk-capsula-mini"

// the boards boardOf knows: a file in one of them may hold the names its entries allow
var allBoards = []string{pickleBoard, midiBoard, microBoard, rpiBoard, vkBoard}

// boardNames are the boards' hardware names, each with the boards that may hold it, comments included: any other
// file that names one fails. the stock library's own file layout (vqe_models) is the library's convention, not a
// board's, and stays allowed
var boardNames = []struct {
	boards []string
	names  []string
}{
	// linux's own nodes and key codes, which a board that reads its hardware names
	{[]string{pickleBoard, midiBoard, microBoard, rpiBoard, vkBoard}, []string{
		"/dev/input", "/sys/class/input", "KEY_VOLUMEUP", "KEY_VOLUMEDOWN",
	}},
	{[]string{pickleBoard, midiBoard, microBoard, rpiBoard, vkBoard}, []string{"/proc/device-tree"}},
	{yandexBoards, []string{
		// devices, nodes and sysfs
		"/dev/fb", "/dev/snd",
		// the vendor roots, files and launcher
		"cadence-stock", "/system/vendor", "libyandex_evil", "activation/alisa", "quasar_launcher2",
		// key codes
		"KEY_VOICECOMMAND", "KEY_MUTE", "SW_MUTE_DEVICE",
		"gpio-keys", "/sys/class/graphics", "updatesd",
		// quoted names
		`"Master"`,
	}},
	{[]string{pickleBoard}, []string{
		"aw933xx", "ltr308als01", "pickle:white", "Yandex Pickle", `"pickle"`,
	}},
	{[]string{pickleBoard, microBoard, vkBoard}, []string{"/sys/class/leds"}},
	// both boards' ambient light sensors are iio devices
	{[]string{pickleBoard, vkBoard}, []string{"/sys/bus/iio"}},
	// with the midi's framebuffer geometry as its sysfs spells it, and its stock's OTA parts its hardening names
	{[]string{midiBoard}, []string{
		"midi_touch", "yandexmidi", "Yandex Station Midi", `"89,1"`, `"356"`, "device_ota_update",
	}},
	{[]string{microBoard}, []string{
		"sar0_pad0", "yandexmicro", "Yandex Station Micro", `"3,1"`,
	}},
}

// yandexKeyCodes are the yandex boards' key codes a go literal could spell (in any base): KEY_VOICECOMMAND,
// KEY_MUTE, KEY_VOLUMEDOWN, KEY_VOLUMEUP. SW_MUTE_DEVICE (14) is too common a number to ban; its name is banned
var yandexKeyCodes = map[int64]string{0x246: "KEY_VOICECOMMAND", 113: "KEY_MUTE", 114: "KEY_VOLUMEDOWN", 115: "KEY_VOLUMEUP"}

// boardOf is the board whose package holds rel, "" for any other file
func boardOf(rel string) string {
	for _, b := range allBoards {
		if rel == b || strings.HasPrefix(rel, b+"/") {
			return b
		}
	}
	return ""
}

// scanGo returns the non-test go files under root (device or pkg) that name a board's hardware outside that board,
// and how many it read. generated code numbers its own messages, so only hand-written code is checked for key codes
func scanGo(t *testing.T, root string) (violations []string, scanned int) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel := relSlash(t, root, path)
		board := boardOf(rel)
		scanned++
		violations = append(violations, named(t, path, rel, board)...)
		if !strings.HasPrefix(rel, "gen/") && board == "" {
			violations = append(violations, keyCodes(t, path, rel)...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return violations, scanned
}

// keyCodes are the integer literals in a go file that spell one of the yandex boards' key codes
func keyCodes(t *testing.T, path, rel string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.INT {
			return true
		}
		if v, err := strconv.ParseInt(strings.ReplaceAll(lit.Value, "_", ""), 0, 64); err == nil && yandexKeyCodes[v] != "" {
			out = append(out, rel+" spells "+yandexKeyCodes[v]+" as "+lit.Value)
		}
		return true
	})
	return out
}

// scanScripts returns the device's sources that are not go (the boot wrapper, the boards' hooks, the inittab swap),
// test data and go's module files left out, that name a board's hardware outside that board
func scanScripts(t *testing.T, root string) (violations []string, scanned int) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "fixtures" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") || d.Name() == "go.mod" || d.Name() == "go.sum" {
			return nil
		}
		rel := relSlash(t, root, path)
		scanned++
		violations = append(violations, named(t, path, rel, boardOf(rel))...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return violations, scanned
}

// scanCpp returns the media sources outside its tests, generated code and vendored code that name a board's
// hardware: audiod takes every board value from Configure
func scanCpp(t *testing.T, media string) (violations []string, scanned int) {
	t.Helper()
	skip := []string{"gen", "third_party", "tests"}
	err := filepath.WalkDir(media, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := relSlash(t, media, path)
		if d.IsDir() {
			if slices.Contains(skip, rel) {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".c", ".cc", ".cpp", ".h", ".hpp", ".cmake":
		default:
			if d.Name() != "CMakeLists.txt" {
				return nil
			}
		}
		scanned++
		violations = append(violations, named(t, path, rel, "")...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return violations, scanned
}

// named are the hardware names in the file at path that the board holding it ("" for none) may not hold
func named(t *testing.T, path, rel, board string) []string {
	t.Helper()
	raw := read(t, path)
	var out []string
	for _, e := range boardNames {
		if slices.Contains(e.boards, board) {
			continue
		}
		for _, name := range e.names {
			if strings.Contains(raw, name) {
				out = append(out, rel+" names "+name)
			}
		}
	}
	return out
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // the repo's own sources
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func relSlash(t *testing.T, base, path string) string {
	t.Helper()
	rel, err := filepath.Rel(base, path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel)
}

func TestBoardNamesStayInTheirBoards(t *testing.T) {
	device, pkg, media := filepath.Join("..", ".."), filepath.Join("..", "..", "..", "pkg"), filepath.Join("..", "..", "..", "media")
	goBad, goScanned := scanGo(t, device)
	pkgBad, pkgScanned := scanGo(t, pkg)
	cppBad, cppScanned := scanCpp(t, media)
	shBad, shScanned := scanScripts(t, device)
	for _, v := range slices.Concat(goBad, pkgBad, cppBad, shBad) {
		t.Error(v)
	}
	// a wrong root or a layout that moved would leave nothing to scan and pass
	if goScanned < 50 || pkgScanned < 5 || cppScanned < 50 || shScanned < 4 {
		t.Fatalf("scanned %d device, %d pkg, %d c++ and %d other device files", goScanned, pkgScanned, cppScanned, shScanned)
	}
	// and each board must still hold names of its own, or the scan would look for names nothing uses
	for _, b := range yandexBoards {
		dir := filepath.Join(device, filepath.FromSlash(b))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") &&
				ownName(read(t, filepath.Join(dir, e.Name())), b) {
				found = true
			}
		}
		if !found {
			t.Errorf("self-check: no name of its own in %s", b)
		}
	}
}

// ownName says raw holds a name that board alone may hold
func ownName(raw, board string) bool {
	for _, e := range boardNames {
		if slices.Equal(e.boards, []string{board}) && slices.ContainsFunc(e.names, func(n string) bool { return strings.Contains(raw, n) }) {
			return true
		}
	}
	return false
}

// a planted name or key code is found outside its boards, in go (device and pkg) and in c++, and allowed inside
// them, in go tests and in media's tests and generated code; one board's own names are found in the other
func TestPlantedBoardNameFails(t *testing.T) {
	root := t.TempDir()
	plant := func(rel, text string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plant("device/internal/core/core.go", "package core\n\n// reads the ltr308als01 light sensor\n")
	plant("device/boards/sim/sim.go", "package sim\n\nconst control = \"Master\"\n")
	plant("device/internal/core/core_test.go", "package core\n\nconst node = \"/dev/fb1\"\n")
	plant("device/internal/input/keys.go", "package input\n\nconst action, up = 0x246, 1_15\n")
	plant("device/internal/leds/ring.go", "package leds\n\n// the gpio-keys device\n")
	plant("device/boards/yandex-pickle/lights.go", "package pickle\n\nconst ring = \"/dev/fb1\"\n")
	plant("device/boards/yandex-pickle/buttons.go", "package pickle\n\nconst keyVoiceCommand = 0x246\n")
	plant("device/boards/yandex-pickle/installer/profile.go", "package installer\n\nvar daemons = []string{\"quasar_launcher2\"}\n")
	plant("device/boards/yandex-pickle/fingerprint.go", "package pickle\n\nconst stride = \"356\"\n")
	plant("device/boards/yandex-midi/buttons.go", "package midi\n\nconst keys, action, mixer = \"midi_touch\", 0x246, \"Master\"\n")
	plant("device/boards/yandex-midi/fingerprint.go", "package midi\n\n// not the aw933xx\n")
	plant("device/boards/yandex-midix/x.go", "package midix\n\nconst keys = \"midi_touch\"\n")
	plant("pkg/wire/wire.go", "package wire\n\n// reads /dev/input\n")
	plant("pkg/gen/cadence/v2/link.pb.go", "package v2\n\nvar _ = msgTypes[113]\n")
	plant("media/audiod/capture.cpp", "// opens aw933xx\nint x;\n")
	plant("media/audiod/vqe.cpp", "// the yandexmidi preset\nint y;\n")
	plant("media/lib/src/ring.cpp", "constexpr const char* kModel = \"Yandex Station Midi\";\n")
	plant("media/lib/src/stock.h", "#define ROOT \"/system/vendor/quasar\"\n")
	plant("media/tests/capture.test.cpp", "const char* kRoot = \"/system/vendor/quasar\";\n")
	plant("media/gen/audiod.pb.h", "// KEY_MUTE gpio-keys\n")
	plant("device/internal/install/files/cadence-run", "#!/bin/sh\nfor led in /sys/class/leds/pickle:white:clock*; do :; done\n")
	plant("device/boards/sim/files/board-hook", "board_blank() { head -c 18 /dev/zero > /dev/fb0; } # /sys/class/graphics\n")
	plant("device/boards/yandex-midi/files/board-hook", "VENDOR=/system/vendor/quasar # quasar_launcher2\nFB=/sys/class/graphics/fb0\n")
	plant("device/boards/sim/testdata/configure.txtpb", "control: \"Master\"\n")
	goBad, goScanned := scanGo(t, filepath.Join(root, "device"))
	pkgBad, pkgScanned := scanGo(t, filepath.Join(root, "pkg"))
	cppBad, cppScanned := scanCpp(t, filepath.Join(root, "media"))
	if want := []string{
		"boards/sim/sim.go names \"Master\"",
		"boards/yandex-midi/fingerprint.go names aw933xx",
		"boards/yandex-midix/x.go names midi_touch",
		"boards/yandex-pickle/fingerprint.go names \"356\"",
		"internal/core/core.go names ltr308als01",
		"internal/input/keys.go spells KEY_VOICECOMMAND as 0x246", "internal/input/keys.go spells KEY_VOLUMEUP as 1_15",
		"internal/leds/ring.go names gpio-keys",
	}; !slices.Equal(goBad, want) || goScanned != 11 {
		t.Fatalf("go: %q in %d files, want %q in 11", goBad, goScanned, want)
	}
	// generated code numbers its own messages
	if want := []string{"wire/wire.go names /dev/input"}; !slices.Equal(pkgBad, want) || pkgScanned != 2 {
		t.Fatalf("pkg: %q in %d files, want %q in 2", pkgBad, pkgScanned, want)
	}
	if want := []string{"audiod/capture.cpp names aw933xx", "audiod/vqe.cpp names yandexmidi",
		"lib/src/ring.cpp names Yandex Station Midi", "lib/src/stock.h names /system/vendor"}; !slices.Equal(cppBad, want) || cppScanned != 4 {
		t.Fatalf("c++: %q in %d files, want %q in 4", cppBad, cppScanned, want)
	}
	// the wrapper is core, each hook its board's; test data is not scanned
	shBad, shScanned := scanScripts(t, filepath.Join(root, "device"))
	if want := []string{"boards/sim/files/board-hook names /dev/fb", "boards/sim/files/board-hook names /sys/class/graphics",
		"internal/install/files/cadence-run names pickle:white", "internal/install/files/cadence-run names /sys/class/leds"}; !slices.Equal(shBad, want) || shScanned != 3 {
		t.Fatalf("scripts: %q in %d files, want %q in 3", shBad, shScanned, want)
	}
}

// testAudioSet returns where the non-test go files under device set a trigger's Test flag outside the sim, and how many
// it read
func testAudioSet(t *testing.T, device string) (violations []string, scanned int) {
	t.Helper()
	err := filepath.WalkDir(device, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel := relSlash(t, device, path)
		scanned++
		if strings.HasPrefix(rel, "boards/sim/") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		inAudio := f.Name.Name == "audio"
		inSession := strings.HasPrefix(rel, "internal/session/")
		sets := false
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit:
				for _, lit := range triggerLits(n, inAudio) {
					sets = sets || setsTest(lit, inSession)
				}
			case *ast.AssignStmt:
				// no types here: any field named Test, which only the trigger has in the device
				for _, l := range n.Lhs {
					if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "Test" {
						sets = true
					}
				}
			}
			return true
		})
		if sets {
			violations = append(violations, rel+" sets Test")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return violations, scanned
}

// isTrigger says the type expression names audio.Trigger (Trigger inside package audio)
func isTrigger(e ast.Expr, inAudio bool) bool {
	switch e := e.(type) {
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		return ok && x.Name == "audio" && e.Sel.Name == "Trigger"
	case *ast.Ident:
		return inAudio && e.Name == "Trigger"
	case *ast.StarExpr:
		return isTrigger(e.X, inAudio)
	}
	return false
}

// triggerLits are the trigger literals lit is or holds with their type left out ([]audio.Trigger{{...}})
func triggerLits(lit *ast.CompositeLit, inAudio bool) []*ast.CompositeLit {
	if lit.Type != nil && isTrigger(lit.Type, inAudio) {
		return []*ast.CompositeLit{lit}
	}
	var elem ast.Expr
	switch ty := lit.Type.(type) {
	case *ast.ArrayType:
		elem = ty.Elt
	case *ast.MapType:
		elem = ty.Value
	}
	if elem == nil || !isTrigger(elem, inAudio) {
		return nil
	}
	var out []*ast.CompositeLit
	for _, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			e = kv.Value
		}
		if inner, ok := e.(*ast.CompositeLit); ok && inner.Type == nil {
			out = append(out, inner)
		}
	}
	return out
}

// setsTest says the literal sets Test, other than as the session's own inherited flag (s.injected)
func setsTest(lit *ast.CompositeLit, inSession bool) bool {
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Test" {
			continue
		}
		if sel, ok := kv.Value.(*ast.SelectorExpr); ok && inSession && sel.Sel.Name == "injected" {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "s" {
				continue
			}
		}
		return true
	}
	return false
}

// test audio (Trigger.Test) comes from the sim's say file alone, and a follow-up inherits it from its own session: a
// real board that set it would hear its next follow-up with its real microphone after test input (M6a-R4)
func TestOnlyTheSimSetsTestAudio(t *testing.T) {
	bad, scanned := testAudioSet(t, filepath.Join("..", ".."))
	for _, v := range bad {
		t.Error(v)
	}
	if scanned < 50 {
		t.Fatalf("scanned %d device files", scanned)
	}
}

// a planted Test is found outside the sim, in a literal or an assignment, and allowed in the sim, in the session's
// inherited flag and in tests
func TestPlantedTestAudioFails(t *testing.T) {
	root := t.TempDir()
	plant := func(rel, text string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const imp = "package x\n\nimport \"git.shino.boo/devilreef/cadence/device/internal/audio\"\n\n"
	plant("boards/sim/say.go", imp+"var _ = audio.Trigger{Fixture: \"f\", Test: true}\n")
	plant("internal/session/controller.go", imp+"func f(s struct{ injected bool }) audio.Trigger {\n"+
		"\treturn audio.Trigger{Test: s.injected}\n}\n")
	plant("internal/session/other.go", imp+"var _ = audio.Trigger{Test: true}\n")
	plant("boards/yandex-pickle/inject.go", imp+"var _ = []audio.Trigger{{Kind: 1}, audio.Trigger{Test: on()}}\n\nfunc on() bool { return true }\n")
	plant("internal/core/seam.go", imp+"func f(t audio.Trigger) { t.Test = true }\n")
	plant("internal/audio/own.go", "package audio\n\nvar _ = Trigger{Test: true}\n")
	plant("internal/core/seam_test.go", imp+"var _ = audio.Trigger{Test: true}\n")
	bad, scanned := testAudioSet(t, root)
	if want := []string{
		"boards/yandex-pickle/inject.go sets Test", "internal/audio/own.go sets Test", "internal/core/seam.go sets Test",
		"internal/session/other.go sets Test",
	}; !slices.Equal(bad, want) || scanned != 6 {
		t.Fatalf("%q in %d files, want %q in 6", bad, scanned, want)
	}
}

// simSeams are the names of the sim's log probe, the file the e2e writes test lines with secrets into and the source
// they ship as: a real board that read it would ship whatever a file there holds
var simSeams = []string{"logprobe", "sim-probe"}

// seamsNamed returns where the non-test go files under device outside the sim spell one of the sim's seams in a
// string literal, and how many it read
func seamsNamed(t *testing.T, device string) (violations []string, scanned int) {
	t.Helper()
	err := filepath.WalkDir(device, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel := relSlash(t, device, path)
		scanned++
		if strings.HasPrefix(rel, "boards/sim/") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, _ := strconv.Unquote(lit.Value)
			for _, s := range simSeams {
				if strings.Contains(v, s) {
					violations = append(violations, rel+" names "+s)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return violations, scanned
}

// the sim alone reads its log probe
func TestOnlyTheSimReadsTheLogProbe(t *testing.T) {
	bad, scanned := seamsNamed(t, filepath.Join("..", ".."))
	for _, v := range bad {
		t.Error(v)
	}
	if scanned < 50 {
		t.Fatalf("scanned %d device files", scanned)
	}
}

// a planted seam name is found in a literal outside the sim, and allowed in the sim, in comments and in tests
func TestPlantedLogProbeFails(t *testing.T) {
	root := t.TempDir()
	plant := func(rel, text string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plant("boards/sim/logprobe.go", "package sim\n\nconst name, source = \"logprobe\", \"sim-probe\"\n")
	plant("boards/yandex-pickle/probe.go", "package pickle\n\nconst path = \"/data/cadence/logprobe\"\n")
	plant("internal/telemetry/probe.go", "package telemetry\n\n// unlike the sim's logprobe\nconst source = `sim-probe`\n")
	plant("internal/core/probe_test.go", "package core\n\nconst name = \"logprobe\"\n")
	bad, scanned := seamsNamed(t, root)
	if want := []string{"boards/yandex-pickle/probe.go names logprobe", "internal/telemetry/probe.go names sim-probe"}; !slices.Equal(bad, want) || scanned != 3 {
		t.Fatalf("%q in %d files, want %q in 3", bad, scanned, want)
	}
}
