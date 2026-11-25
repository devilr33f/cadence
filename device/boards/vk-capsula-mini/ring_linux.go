//go:build linux

package vkcapsula

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	topledsSet   = 0x40107a01
	topledsCount = 0x80047a02
)

type topleds struct{ fd int }

// the kernel's struct: start and count of channels, then a user pointer to their bytes
type topledsData struct {
	start, count uint32
	data         *byte
}

// openTopleds is nil unless the node answers the count ioctl with 36 channels
func openTopleds(path string) *topleds {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	var n uint32
	// the kernel fills n through the pointer, which the raw ioctl takes as a uintptr
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), topledsCount, uintptr(unsafe.Pointer(&n))) //nolint:gosec
	if e != 0 || n != ringChannelCount {
		_ = unix.Close(fd)
		return nil
	}
	return &topleds{fd: fd}
}

func (t *topleds) set(ch *[ringChannelCount]byte) error {
	d := topledsData{start: 0, count: ringChannelCount, data: &ch[0]}
	// the ioctl takes a pointer to d, which holds a pointer to ch; both stay alive across the call
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(t.fd), topledsSet, uintptr(unsafe.Pointer(&d))) //nolint:gosec
	runtime.KeepAlive(ch)
	runtime.KeepAlive(&d)
	if e != 0 {
		return errors.New("topleds set failed")
	}
	return nil
}

func (t *topleds) close() { _ = unix.Close(t.fd) }
