package audiod

import "syscall"

// audiod dies with cadenced (pdeathsig) and gets its own process group, so a
// terminal's signals reach only cadenced, which stops it in order
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
}
