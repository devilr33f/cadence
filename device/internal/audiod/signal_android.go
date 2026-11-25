//go:build android

package audiod

import (
	"os"
	"syscall"
)

// android's app seccomp filter kills a process that calls pidfd_send_signal, which os.Process.Signal and Kill use
// once the child has a pidfd (go1.27, GOOS=android too), so the child is signalled by pid; its pid is freed only
// when spawn's Wait reaps it, just before exited closes, which leaves that short gap for a reused pid
func (p *proc) signal(sig syscall.Signal) error {
	select {
	case <-p.exited:
		return os.ErrProcessDone
	default:
	}
	return syscall.Kill(p.cmd.Process.Pid, sig)
}
