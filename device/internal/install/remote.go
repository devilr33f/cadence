package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Runner runs POSIX sh on the device. A script's own failure is its Result's code; an error means the transport
// failed and the device's state is unknown.
type Runner interface {
	Run(ctx context.Context, script string) (Result, error)
	// Push copies a local file to path on the device.
	Push(ctx context.Context, local, path string) error
	// String names the transport in messages.
	String() string
}

// Result is a script's output and exit status.
type Result struct {
	Out  []byte
	Code int
}

// rcMarker ends every command: adbd has no shell protocol, so adb shell exits 0 whatever ran, and the status comes
// back as the last line instead (as v1's remote.sh). none means the session died
const rcMarker = "cadence-rc="

// wrap runs script in a subshell, so its own exit still reaches the marker, which goes on a line of its own
func wrap(script string) string {
	return "( " + script + " ); rc=$?; echo; echo " + rcMarker + "$rc"
}

// unwrap splits the marker off a wrapped command's output; the transport's carriage returns are dropped
func unwrap(out []byte) (Result, bool) {
	out = bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n"))
	out = bytes.TrimSuffix(out, []byte("\n"))
	i := bytes.LastIndexByte(out, '\n')
	if i < 0 {
		return Result{}, false
	}
	last := string(out[i+1:])
	code, err := strconv.Atoi(strings.TrimPrefix(last, rcMarker))
	if !strings.HasPrefix(last, rcMarker) || err != nil {
		return Result{}, false
	}
	return Result{Out: out[:i], Code: code}, true
}

// quote makes s one sh word
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// execFunc runs a host program; a seam so tests never run ssh or adb
type execFunc func(ctx context.Context, name string, args []string, env []string, stdin io.Reader) (stdout, stderr []byte, err error)

func hostExec(ctx context.Context, name string, args []string, env []string, stdin io.Reader) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // ssh or adb with the installer's own arguments
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var out, errOut bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, &out, &errOut
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// transportError keeps the last line the transport program wrote, which names its own failure (ssh's host key
// check, adb's missing device); scripts never print secrets
func transportError(name string, stderr []byte, err error) error {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(stderr), "\r", "")), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return fmt.Errorf("%s: %s", name, last)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return errors.New(name + ": the session ended before the command did")
}

// Answering returns the runners that answer, in order, and why each of the others did not.
func Answering(ctx context.Context, runners []Runner) ([]Runner, []error) {
	var ok []Runner
	var errs []error
	for _, r := range runners {
		res, err := r.Run(ctx, "true")
		switch {
		case err != nil:
			errs = append(errs, err)
		case res.Code != 0:
			errs = append(errs, fmt.Errorf("%s: true exited %d", r, res.Code))
		default:
			ok = append(ok, r)
		}
	}
	return ok, errs
}
