package deviceops

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// transport is one way the server itself drives a device: run a shell command, write a file,
// read a file's sha256. The adb one speaks to an adb server (the pod's sidecar) through gadb, the
// ssh one dials the board's host; the link needs none of this (the daemon runs the plan).
type transport interface {
	probe(ctx context.Context, acc access) error
	// exec runs cmd and answers its exit status and merged output. adbd has no shell protocol:
	// the adb transport wraps the command with a status marker of its own.
	exec(ctx context.Context, acc access, cmd string, wait time.Duration) (code int, out string, err error)
	writeFile(ctx context.Context, acc access, path string, mode uint32, data []byte) error
	readHash(ctx context.Context, acc access, path string) (string, error)
}

// The adb status marker (the installer's remote.sh convention: adbd answers 0 whatever ran, so
// the status rides the last line instead).
const rcMarker = "cadence-rc="

func wrapScript(script string) string {
	return "( " + script + " ); rc=$?; echo; echo " + rcMarker + "$rc"
}

func unwrapScript(out string) (code int, body string, ok bool) {
	out = strings.ReplaceAll(out, "\r\n", "\n")
	out = strings.TrimSuffix(out, "\n")
	i := strings.LastIndex(out, "\n"+rcMarker)
	line := ""
	switch {
	case i >= 0:
		line, body = out[i+1:], out[:i]
	case strings.HasPrefix(out, rcMarker):
		line, body = out, ""
	default:
		return 0, out, false
	}
	code, err := strconv.Atoi(strings.TrimPrefix(line, rcMarker))
	if err != nil {
		return 0, body, false
	}
	return code, body, true
}

var errNoMarker = errors.New("deviceops: the shell session died before its status")
