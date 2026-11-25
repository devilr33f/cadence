// Package busyboxtest runs shell commands in a throwaway busybox container, the shell a speaker has: the installer's
// commands and the boot wrapper are tested on it.
package busyboxtest

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Image is busybox 1.37.0 built against glibc, as a speaker's is, pinned by its index digest.
const Image = "busybox:1.37.0-glibc@sha256:42a9ff19af33811ee466b24c66384461d37c32dcc28d1d27d49b8aca816bd5ee"

// Container is one running container, removed when the test ends.
type Container struct {
	t  testing.TB
	id string
}

// Start runs a container. With -short the test is skipped; without a docker cli it is skipped too, except in ci
// (CI set), where it fails.
func Start(t testing.TB) *Container {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: no busybox container")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		if missing(os.Getenv("CI")) == "fail" {
			t.Fatal("no docker cli: the busybox tests need one in ci")
		}
		t.Skip("no docker cli: the busybox tests need one")
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "cadence-install-test-" + hex.EncodeToString(suffix)
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "--network", "none", Image, "sleep", "600").CombinedOutput() //nolint:gosec // fixed image, generated name
	if err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	c := &Container{t: t, id: name}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() }) //nolint:gosec // the container started here
	return c
}

// missing is what a test does without a docker cli: skip on a developer's machine, fail in ci
func missing(ci string) string {
	if ci != "" {
		return "fail"
	}
	return "skip"
}

// Sh runs script with sh in the container, stdin as its input, and returns its output and status.
func (c *Container) Sh(script string, stdin []byte) (stdout, stderr []byte, code int) {
	c.t.Helper()
	cmd := exec.Command("docker", "exec", "-i", c.id, "sh", "-c", script) //nolint:gosec // the test's own script
	var out, errOut bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		c.t.Fatalf("docker exec: %v", err)
	}
	return out.Bytes(), errOut.Bytes(), code
}

// ShTTY runs script with sh on a pseudo-terminal and no input, as an old adbd runs `adb shell <command>`, and returns
// what the terminal carried (stdout and stderr together, with carriage returns).
func (c *Container) ShTTY(script string) []byte {
	c.t.Helper()
	out, err := exec.Command("docker", "exec", "-t", c.id, "sh", "-c", script).Output() //nolint:gosec // the test's own script
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		c.t.Fatalf("docker exec -t: %v", err)
	}
	return out
}

// MustSh runs script and fails the test unless it exits 0.
func (c *Container) MustSh(script string) []byte {
	c.t.Helper()
	out, errOut, code := c.Sh(script, nil)
	if code != 0 {
		c.t.Fatalf("%s: status %d: %s%s", script, code, out, errOut)
	}
	return out
}

// Put writes data to path in the container with mode, its directory created first.
func (c *Container) Put(path string, data []byte, mode string) {
	c.t.Helper()
	q := quote(path)
	if _, errOut, code := c.Sh(`mkdir -p "$(dirname `+q+`)" && cat > `+q+` && chmod `+quote(mode)+" "+q, data); code != 0 {
		c.t.Fatalf("put %s: %s", path, errOut)
	}
}

// Tree writes files (absolute path to content) in one go, each with mode, as a tar that busybox unpacks at /.
func (c *Container) Tree(files map[string]string, mode int64) {
	c.t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for p, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: strings.TrimPrefix(p, "/"), Mode: mode, Size: int64(len(data))}); err != nil {
			c.t.Fatal(err)
		}
		if _, err := tw.Write([]byte(data)); err != nil {
			c.t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		c.t.Fatal(err)
	}
	if out, errOut, code := c.Sh("tar -xf - -C /", buf.Bytes()); code != 0 {
		c.t.Fatalf("tree: %s%s", out, errOut)
	}
}

// CapsulaBootApplets is what the VK Capsula Mini's stock busybox has on the boot PATH: no sed, od, cmp, setsid or flock,
// and a head without -c.
var CapsulaBootApplets = []string{
	"[", "[[", "ash", "amixer", "basename", "cat", "chmod", "cp", "cut", "date", "dd", "df", "echo", "expr", "false",
	"grep", "head", "hexdump", "kill", "killall", "ln", "logger", "ls", "mkdir", "mkfifo", "mv", "nohup", "pgrep",
	"pidof", "pkill", "printf", "readlink", "rm", "seq", "sh", "sha256sum", "sleep", "sort", "start-stop-daemon",
	"sync", "tail", "tar", "test", "touch", "tr", "true", "uniq", "usleep", "wc",
}

// StockBin makes dir hold a symlink to busybox for each applet the test image's busybox provides and nothing else, so
// a command run with PATH=dir sees a minimal applet set, not the image's full one. a name the image lacks (amixer,
// which the tests fake through CADENCE_AMIXER, or usleep) is skipped: a link to it would resolve to the wrong applet
func (c *Container) StockBin(dir string, applets []string) {
	c.t.Helper()
	q := quote(dir)
	script := "mkdir -p " + q + " && have=$(/bin/busybox --list) && for a in " + quoteAll(applets) +
		`; do case "
$have
" in *"
$a
"*) ln -sf /bin/busybox ` + q + `/"$a" ;; esac; done`
	c.MustSh(script)
}

func quoteAll(s []string) string {
	q := make([]string, len(s))
	for i, a := range s {
		q[i] = quote(a)
	}
	return strings.Join(q, " ")
}

func quote(s string) string {
	return "'" + string(bytes.ReplaceAll([]byte(s), []byte("'"), []byte(`'\''`))) + "'"
}
