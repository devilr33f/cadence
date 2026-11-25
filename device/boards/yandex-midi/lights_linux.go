package midi

import (
	"strconv"
	"syscall"
	"unsafe"
)

// <linux/fb.h>
const (
	fbiogetVScreenInfo = 0x4600
	fbiogetFScreenInfo = 0x4602
)

// fbVarScreenInfo is struct fb_var_screeninfo: forty 32-bit words on every cpu, of which the geometry's are named
type fbVarScreenInfo struct {
	xres, yres               uint32
	xresVirtual, yresVirtual uint32
	xoffset, yoffset         uint32
	bitsPerPixel             uint32
	_                        [33]uint32
}

// fbFixScreenInfo is struct fb_fix_screeninfo. Its two unsigned longs follow the word size of the process, so
// line_length sits at 44 for the midi's 32-bit userland (which a 64-bit kernel converts the struct for) and at 48 on
// arm64.
type fbFixScreenInfo struct {
	id                            [16]byte
	smemStart                     uintptr
	smemLen, typ, typeAux, visual uint32
	xpanstep, ypanstep, ywrapstep uint16
	lineLength                    uint32
	mmioStart                     uintptr
	mmioLen, accel                uint32
	capabilities                  uint16
	reserved                      [2]uint16
}

// the layouts the kernel writes, checked whenever the package compiles (make cross builds arm v7): fb_var_screeninfo
// is 160 bytes with bits_per_pixel at 24 on every cpu; fb_fix_screeninfo is 68 bytes with line_length at 44 for a
// 32-bit process, 80 and 48 for a 64-bit one. a constant that would go negative fails the build
const (
	wide        = strconv.IntSize / 64
	fbVarSize   = 160
	fbVarBitsAt = 24
	fbFixSize   = 68 + 12*wide
	fbFixLineAt = 44 + 4*wide

	_ = unsafe.Sizeof(fbVarScreenInfo{}) - fbVarSize
	_ = fbVarSize - unsafe.Sizeof(fbVarScreenInfo{})
	_ = unsafe.Offsetof(fbVarScreenInfo{}.bitsPerPixel) - fbVarBitsAt
	_ = fbVarBitsAt - unsafe.Offsetof(fbVarScreenInfo{}.bitsPerPixel)
	_ = unsafe.Sizeof(fbFixScreenInfo{}) - fbFixSize
	_ = fbFixSize - unsafe.Sizeof(fbFixScreenInfo{})
	_ = unsafe.Offsetof(fbFixScreenInfo{}.lineLength) - fbFixLineAt
	_ = fbFixLineAt - unsafe.Offsetof(fbFixScreenInfo{}.lineLength)
)

// fbNode is the real framebuffer node
type fbNode struct{ fd int }

func openFramebuffer(path string) (framebuffer, error) {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &fbNode{fd: fd}, nil
}

func (n *fbNode) geometry() (fbGeometry, error) {
	// room past each struct, so a driver that fills more than this layout holds writes nothing it does not own
	var v struct {
		info fbVarScreenInfo
		_    [64]byte
	}
	var f struct {
		info fbFixScreenInfo
		_    [64]byte
	}
	if err := ioctl(n.fd, fbiogetVScreenInfo, unsafe.Pointer(&v)); err != nil { //nolint:gosec // the kernel fills v, larger than the struct it writes
		return fbGeometry{}, err
	}
	if err := ioctl(n.fd, fbiogetFScreenInfo, unsafe.Pointer(&f)); err != nil { //nolint:gosec // the kernel fills f, larger than the struct it writes
		return fbGeometry{}, err
	}
	return fbGeometry{xres: v.info.xres, yres: v.info.yres, bitsPerPixel: v.info.bitsPerPixel, lineLength: f.info.lineLength}, nil
}

func ioctl(fd int, request uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// pwrite is the raw call: an interruption comes back as EINTR, for the frame's loop to retry
func (n *fbNode) pwrite(p []byte, off int64) (int, error) { return syscall.Pwrite(n.fd, p, off) }

func (n *fbNode) close() error { return syscall.Close(n.fd) }
