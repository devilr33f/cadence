package install

import (
	"context"
	"errors"
	"io/fs"
)

// Device is what the installer does on the speaker, one command each. Paths are the device's own, absolute. Shell
// runs them as POSIX sh over a Runner; tests use an in-memory device.
type Device interface {
	// Probe succeeds when the device answers.
	Probe(ctx context.Context) error

	// Read returns a small file's bytes (at most maxRead); ok is false when it does not exist.
	Read(ctx context.Context, path string) (data []byte, ok bool, err error)
	Exists(ctx context.Context, path string) (bool, error)
	// List returns a directory's entry names, nil when it does not exist.
	List(ctx context.Context, dir string) ([]string, error)
	// Sum returns a file's sha256 in hex; ok is false when it does not exist.
	Sum(ctx context.Context, path string) (sum string, ok bool, err error)
	// Processes returns only the processes whose first or second argument is under the data root, chosen on the
	// device: no other command line (a key passed as an argument) crosses the transport.
	Processes(ctx context.Context) ([]Proc, error)
	// Running reports whether a program of that name runs.
	Running(ctx context.Context, name string) (bool, error)
	// RunningFrom returns the processes whose executable, as /proc/<pid>/exe resolves it, lies under dir: their pid
	// and executable only, never a command line.
	RunningFrom(ctx context.Context, dir string) ([]Proc, error)
	// Executable reports whether path is a regular file that may be run.
	Executable(ctx context.Context, path string) (bool, error)
	FreeKB(ctx context.Context, path string) (int64, error)
	// UsedKB is the room the paths take with everything under them, as du counts it; a missing one takes none.
	UsedKB(ctx context.Context, paths ...string) (int64, error)
	// Missing returns the tools the device lacks.
	Missing(ctx context.Context, tools []string) ([]string, error)
	// LogCount counts the log's lines holding needle after its last line holding marker (every one without such a
	// line); a missing log has none.
	LogCount(ctx context.Context, path, marker, needle string) (int, error)
	// Mixer returns the control's playback levels in percent, one per channel.
	Mixer(ctx context.Context, control string) ([]int, error)
	// Level is the level the wrapper applies before a start.
	Level(ctx context.Context, wrapper string) (int, error)

	Mkdir(ctx context.Context, path string, mode fs.FileMode) error
	// Write replaces path with data atomically.
	Write(ctx context.Context, path string, data []byte, mode fs.FileMode) error
	// WriteSecret is Write with mode 600 for data that is never printed.
	WriteSecret(ctx context.Context, path string, data []byte) error
	// Push copies a local file to path.
	Push(ctx context.Context, local, path string) error
	// Copy replaces to with a copy of from atomically.
	Copy(ctx context.Context, from, to string, mode fs.FileMode) error
	// Rename moves from to to, which must not exist.
	Rename(ctx context.Context, from, to string) error
	// Remove removes path and everything under it; only paths under the data root.
	Remove(ctx context.Context, path string) error
	// Unpack unpacks a tarball into dir, which it creates.
	Unpack(ctx context.Context, tarball, dir string) error
	// Check verifies dir against the sha256 lines of manifest, written to sums first.
	Check(ctx context.Context, dir, manifest, sums string) error
	// Signal sends sig to p only if its exe and command line are still p's; sent is false when they are not or it
	// is gone.
	Signal(ctx context.Context, p Proc, sig string) (sent bool, err error)
	// SwapInittab replaces the inittab with staged if the inittab's sha256 is still oldSum and staged's is newSum,
	// then makes init reload it. The root is writable only for the write and read-only again on every exit.
	SwapInittab(ctx context.Context, staged, oldSum, newSum string) error
	// ReloadInit makes init read its table again.
	ReloadInit(ctx context.Context) error
	// Blank turns the lights off through the wrapper.
	Blank(ctx context.Context, wrapper string) error
	// Prestart runs the board's prestart once through the wrapper: what must succeed before every start of cadenced.
	Prestart(ctx context.Context, wrapper string) error
	// StartScript starts a script with sh in the background, detached, so it outlives the session.
	StartScript(ctx context.Context, script string) error
	// StartDaemon starts exe with args in the background, detached, unless a process runs as exe already.
	StartDaemon(ctx context.Context, exe string, args []string) error
	// WriteFile replaces path with data: staged beside it as .<name>.cadence-new and renamed over it only once the
	// staged copy's sha256 is data's; mode is chmod's octal. Nothing at path is removed, so on an overlay root its copy
	// in the lower layer is never whited out.
	WriteFile(ctx context.Context, path string, data []byte, mode string) error
	// CopyFile is WriteFile for a file already on the device, of any size: src is copied beside dst as
	// .<name>.cadence-new and renamed over dst only once the copy's sha256 is src's.
	CopyFile(ctx context.Context, src, dst, mode string) error
	// RunScript runs an init script with sh and one argument, its input /dev/null, and returns what it printed.
	RunScript(ctx context.Context, script, arg string) (string, error)
	// HandOver runs the wrapper's release mode (the board's hook lets go of what it held for cadence, a watchdog's
	// feeder) and then script with start, as one command with nothing between them; the start runs whether or not the
	// release worked, both failures are returned, and the start's output.
	HandOver(ctx context.Context, wrapper, script string) (string, error)
	Enroll(ctx context.Context, e Enrollment) error
}

// Proc is a process: its pid, what /proc/<pid>/exe names and its command line.
type Proc struct {
	Pid  int
	Exe  string
	Args []string
}

// Enrollment is cadenced enroll's command line. The code is read on the device from CodeFile, so it is never part
// of a command.
type Enrollment struct {
	Daemon, Server, CodeFile, Board, DataDir, CAFile, Config string
}

// maxRead caps Read: everything read whole is a small text file
const maxRead = 64 << 10

var (
	// ErrRefused is a device or tarball the installer will not change
	ErrRefused = errors.New("refused")
	// ErrEnrollRefused is a code the server refused
	ErrEnrollRefused = errors.New("enroll code refused")
)
