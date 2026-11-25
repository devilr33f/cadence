package audiod

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

// a child of audiod could keep its stderr open after audiod died
const stderrDrain = time.Second

// minimal environment: cadenced's own never reaches audiod
var minimalEnv = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}

// proc is a spawned audiod, or (cmd nil) the stand-in for an external one, whose exited never closes
type proc struct {
	cmd     *exec.Cmd
	stderr  *os.File
	exited  chan struct{} // closed once the child is reaped
	logDone chan struct{} // closed once its stderr is read to the end
}

func externalProc() *proc {
	return &proc{exited: make(chan struct{}), logDone: make(chan struct{})}
}

func (s *Supervisor) spawn() (*proc, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	args := append([]string{"--socket", s.spec.Socket}, s.spec.Args...)
	cmd := exec.Command(s.spec.Binary, args...) //nolint:gosec // the operator's configured audiod binary
	cmd.Env = s.spec.Env
	if cmd.Env == nil {
		cmd.Env = minimalEnv
	}
	cmd.Stderr = w
	cmd.SysProcAttr = procAttr()
	p := &proc{cmd: cmd, stderr: r, exited: make(chan struct{}), logDone: make(chan struct{})}
	started := make(chan error, 1)
	go func() {
		// pdeathsig fires when the thread that forked the child exits, so that
		// thread stays locked to this goroutine until the child is reaped
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := cmd.Start(); err != nil {
			started <- err
			return
		}
		started <- nil
		_ = cmd.Wait()
		close(p.exited)
	}()
	err = <-started
	_ = w.Close()
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	go func() {
		defer close(p.logDone)
		s.forwardStderr(r)
	}()
	return p, nil
}

// stop asks audiod to exit and kills it after the grace.
func (p *proc) stop(grace time.Duration) {
	if err := p.signal(syscall.SIGTERM); err != nil {
		p.kill()
		return
	}
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-p.exited:
	case <-t.C:
		p.kill()
	}
}

func (p *proc) kill() {
	_ = p.signal(syscall.SIGKILL)
	<-p.exited
}

// reap waits for the stderr reader after the child was reaped; it returns the
// exit code, -1 for a signal.
func (p *proc) reap() int {
	<-p.exited
	t := time.NewTimer(stderrDrain)
	defer t.Stop()
	select {
	case <-p.logDone:
	case <-t.C:
		_ = p.stderr.Close()
		<-p.logDone
	}
	return p.cmd.ProcessState.ExitCode()
}
