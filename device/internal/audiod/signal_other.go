//go:build !android

package audiod

import "syscall"

func (p *proc) signal(sig syscall.Signal) error {
	return p.cmd.Process.Signal(sig)
}
