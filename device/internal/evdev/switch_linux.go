package evdev

import (
	"os"
	"syscall"
	"unsafe"
)

// eviocgsw is EVIOCGSW(n), _IOC(_IOC_READ, 'E', 0x1b, n): the switches' state bits, the same on arm and arm64
func eviocgsw(n int) uintptr { return 2<<30 | uintptr(n)<<16 | 'E'<<8 | 0x1b }

// Switch is the latched position of switch code on an open node; false when the node cannot tell.
func Switch(f *os.File, code uint16) (on, ok bool) {
	if code > swMax {
		return false, false
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return false, false
	}
	var bits [swMax/8 + 1]byte
	var errno syscall.Errno
	// through the raw conn: Fd would turn the node back to blocking reads
	err = rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, eviocgsw(len(bits)), uintptr(unsafe.Pointer(&bits[0]))) //nolint:gosec // the kernel fills bits, sized as the request says
	})
	if err != nil || errno != 0 {
		return false, false
	}
	return bits[code/8]>>(code%8)&1 != 0, true
}
