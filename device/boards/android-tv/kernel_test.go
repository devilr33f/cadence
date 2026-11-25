package androidtv

import (
	"os"
	"path/filepath"
	"testing"
)

// the stick as it is: a 32-bit userland on a 64-bit kernel, whose compat personality renames aarch64 to armv8l for
// every 32-bit process, cadenced included
func TestKernelArm64(t *testing.T) {
	for _, c := range []struct {
		name   string
		signal kernelSignals
		want   bool
		how    string
	}{
		{"armv8l with the personality answered", kernelSignals{uname: "armv8l", personalityUname: "aarch64"}, true, "personality"},
		{"armv8l with the sysctl answered", kernelSignals{uname: "armv8l", sysctlArch: "aarch64"}, true, "arch"},
		{"armv8l with the probe answered", kernelSignals{uname: "armv8l", probe: probeRan}, true, "probe"},
		{"a 64-bit process's own uname", kernelSignals{uname: "aarch64", probe: probeRefused}, true, "uname"},
		{"the answers in order", kernelSignals{uname: "aarch64", personalityUname: "aarch64"}, true, "uname"},
		{"a 32-bit kernel", kernelSignals{uname: "armv7l", personalityUname: "armv7l", sysctlArch: "armv7l", probe: probeRefused}, false, "refused"},
		{"armv8l where nothing else answered and the loader ran", kernelSignals{uname: "armv8l", probe: probeRan}, true, "probe"},
		{"armv8l refused by the probe", kernelSignals{uname: "armv8l", probe: probeRefused}, false, "refused"},
		{"nothing answered", kernelSignals{uname: "armv8l"}, false, "unknown"},
	} {
		if got, how := kernelArm64(c.signal); got != c.want || how != c.how {
			t.Fatalf("%s: kernelArm64(%+v) = %v, %s; want %v, %s", c.name, c.signal, got, how, c.want, c.how)
		}
	}
}

// the app's word is authoritative both ways; without one, the kernel's questions answer on the tests' hosts only
// through the probe
func TestKernelRunsArm64(t *testing.T) {
	if got, how := kernelRunsArm64("aarch64", ""); !got || how != "env" {
		t.Fatalf("env aarch64 = %v, %s", got, how)
	}
	if got, how := kernelRunsArm64("armv7l", ""); got || how != "env" {
		t.Fatalf("env armv7l = %v, %s", got, how)
	}
	// no word, no loader: unknown on a host that cannot ask, which is the point
	if got, _ := kernelRunsArm64("", ""); got {
		t.Fatal("no word and no loader ran arm64")
	}
	// no word, a loader that runs: yes
	loader := executable(t, "#!/bin/sh\nexit 0\n")
	if got, how := kernelRunsArm64("", loader); !got || how != "probe" {
		t.Fatalf("probe = %v, %s", got, how)
	}
}

// the probe asks the kernel itself: a loader that answers at all proves it execs aarch64, ENOEXEC is its refusal,
// and anything else says nothing
func TestProbeArm64(t *testing.T) {
	if got := probeArm64(""); got != probeNone {
		t.Fatalf("no loader = %v", got)
	}
	if got := probeArm64(filepath.Join(t.TempDir(), "no-such-loader")); got != probeNone {
		t.Fatalf("a missing loader = %v", got)
	}
	dir := t.TempDir()
	still := filepath.Join(dir, "still")
	if err := os.WriteFile(still, []byte("no exec bit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := probeArm64(still); got != probeNone {
		t.Fatalf("a loader without the exec bit = %v", got)
	}
	if got := probeArm64(executable(t, "#!/bin/sh\nexit 0\n")); got != probeRan {
		t.Fatalf("a loader that exited 0 = %v", got)
	}
	if got := probeArm64(executable(t, "#!/bin/sh\nexit 3\n")); got != probeRan {
		t.Fatalf("a loader that exited 3 = %v", got)
	}
	// bytes no kernel would run: the 32-bit kernel's answer
	if got := probeArm64(executable(t, "\x7fnot-an-elf-at-all")); got != probeRefused {
		t.Fatalf("an unexecutable binary = %v", got)
	}
}

func executable(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "loader")
	if err := os.WriteFile(file, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return file
}

// the app's soloist paths: its builds and runtime, its scratch beside audiod's run dirs, and the bundle the app
// writes from the system's certificates, alone verifying spotify's cdn
func TestSoloistOptions(t *testing.T) {
	dir, err := os.MkdirTemp("", "androidtv")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	b := &Board{dataDir: dir}
	o := b.soloistOptions()
	if o.Dir != filepath.Join(dir, "soloist") || o.ScratchDir != filepath.Join(dir, "run", "soloist") ||
		o.CAFile != filepath.Join(dir, "ca-bundle.crt") {
		t.Fatalf("paths = %q %q %q", o.Dir, o.ScratchDir, o.CAFile)
	}
	if o.Updates == nil || o.Updates.URL == "" || o.Updates.Machine == 0 || o.Updates.CAFile != o.CAFile {
		t.Fatalf("updates = %+v", o.Updates)
	}
}
