//go:build linux

package cec

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"unsafe"

	"syscall"
)

// <linux/cec.h>: the structs and ioctls the wire takes. cec_msg is 56 bytes on every word size; the caps
// and log-addrs structs only ever run on the pi, but keep their sizes asserted the same way.
//
//nolint:unused // kernel struct layout: fields we never read still hold the offsets and the ioctl's size
type cecMsg struct {
	txTS, rxTS                uint64
	len, timeout, seq, flags  uint32
	msg                       [16]byte
	reply, rxStatus, txStatus uint8
	txArbLost, txNack, txLow  uint8
	txError                   uint8
	_                         uint8
}

//nolint:unused // kernel struct layout
type cecCaps struct {
	driver, name                             [32]byte
	availableLogAddrs, capabilities, version uint32
}

//nolint:unused // kernel struct layout
type cecLogAddrs struct {
	logAddr           [4]uint8
	logAddrMask       uint16
	cecVersion        uint8
	numLogAddrs       uint8
	vendorID          uint32
	flags             uint32
	osdName           [15]uint8
	primaryDeviceType [4]uint8
	logAddrType       [4]uint8
	allDeviceTypes    [4]uint8
	features          [4][12]uint8
}

const (
	_ = uint(unsafe.Sizeof(cecMsg{}) - 56)
	_ = uint(56 - unsafe.Sizeof(cecMsg{}))
	_ = uint(unsafe.Sizeof(cecCaps{}) - 76)
	_ = uint(76 - unsafe.Sizeof(cecCaps{}))
	_ = uint(unsafe.Sizeof(cecLogAddrs{}) - 92)
	_ = uint(92 - unsafe.Sizeof(cecLogAddrs{}))
)

const (
	cecIOCMagic = 'a'
	// CEC_ADAP_G_CAPS = _IOWR('a', 0, struct cec_caps)
	cecGCaps = 3<<30 | uintptr(unsafe.Sizeof(cecCaps{}))<<16 | cecIOCMagic<<8
	// CEC_ADAP_G_PHYS_ADDR = _IOR('a', 1, __u16)
	cecGPhysAddr = 2<<30 | 2<<16 | cecIOCMagic<<8 | 1
	// CEC_ADAP_G_LOG_ADDRS = _IOR('a', 3, struct cec_log_addrs)
	cecGLogAddrs = 2<<30 | uintptr(unsafe.Sizeof(cecLogAddrs{}))<<16 | cecIOCMagic<<8 | 3
	// CEC_ADAP_S_LOG_ADDRS = _IOWR('a', 4, struct cec_log_addrs)
	cecSLogAddrs = 3<<30 | uintptr(unsafe.Sizeof(cecLogAddrs{}))<<16 | cecIOCMagic<<8 | 4
	// CEC_TRANSMIT = _IOWR('a', 5, struct cec_msg)
	cecTransmit = 3<<30 | uintptr(unsafe.Sizeof(cecMsg{}))<<16 | cecIOCMagic<<8 | 5
	// CEC_RECEIVE = _IOWR('a', 6, struct cec_msg)
	cecReceive = 3<<30 | uintptr(unsafe.Sizeof(cecMsg{}))<<16 | cecIOCMagic<<8 | 6
	// CEC_S_MODE = _IOW('a', 9, __u32)
	cecSMode = 1<<30 | 4<<16 | cecIOCMagic<<8 | 9
)

const (
	cecModeInitiator = 0x1
	cecModeFollower  = 0x10

	cecTxStatusOK   = 1 << 0
	cecRxStatusOK   = 1 << 0
	physAddrInvalid = 0xffff
	cecVersion1_4   = 5
	logAddrTypePlay = 3 // CEC_LOG_ADDR_TYPE_PLAYBACK
)

// Linux is the pi's wire: /dev/cec0 as the kernel's cec api takes it. The adapter's logical addresses are
// the deb unit's cec-ctl claim at boot, never a re-claim of ours; only a wire nobody claimed do we take
// (a playback device, as the screen is), so the commands work wherever that unit never ran.
type Linux struct {
	path string
	log  *slog.Logger

	mu      sync.Mutex
	ready   bool
	f       *os.File
	adapter Adapter
	sinks   sinks
}

// OpenLinux is the wire at path (/dev/cec0 on the pi); it opens on Start and answers absent until then.
func OpenLinux(path string, log *slog.Logger) *Linux {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Linux{path: path, log: log}
}

// Info is the adapter as the kernel named it; absent until Start opened it, and after a close.
func (l *Linux) Info() Adapter {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.adapter
}

// Subscribe adds a sink for the frames the wire carries to us (broadcasts, direct messages the kernel
// passed through); replies the transmitter waited for reach it alone.
func (l *Linux) Subscribe(sink func(Frame)) { l.sinks.add(sink) }

// Start opens the wire, sets initiator|follower, reads (or takes) the adapter's address and reads frames
// until ctx ends. A missing device or a refused mode logs once and leaves the bus absent, as a board
// without cec.
func (l *Linux) Start(ctx context.Context, wg *sync.WaitGroup) {
	l.mu.Lock()
	if l.ready {
		l.mu.Unlock()
		return
	}
	f, err := os.OpenFile(l.path, os.O_RDWR, 0)
	if err != nil {
		l.mu.Unlock()
		l.log.Info("cec_absent", "errno", errnoOf(err))
		return
	}
	mode := uint32(cecModeInitiator | cecModeFollower)
	if _, _, errno := ioctl(f.Fd(), cecSMode, &mode); errno != 0 {
		l.mu.Unlock()
		l.log.Warn("cec_failed", "stage", "mode", "errno", int(errno))
		_ = f.Close()
		return
	}
	l.f, l.ready = f, true
	l.mu.Unlock()
	l.readAdapter()
	l.log.Info("cec_started", "phys_addr", l.Info().PhysicalAddress, "log_addr", l.Info().LogicalAddress)
	wg.Go(func() {
		<-ctx.Done()
		l.mu.Lock()
		defer l.mu.Unlock()
		l.ready = false
		l.adapter.Present = false
		_ = l.f.Close()
		l.f = nil
	})
	wg.Go(func() { l.receive(ctx) })
}

// readAdapter takes the caps, the physical address and the claimed logical addresses; a wire nobody
// configured gets our own playback claim, once (needsClaim)
func (l *Linux) readAdapter() {
	var caps cecCaps
	if _, _, errno := ioctl(l.fd(), cecGCaps, &caps); errno != 0 {
		l.log.Warn("cec_failed", "stage", "caps", "errno", int(errno))
		return
	}
	phys := uint16(physAddrInvalid)
	if _, _, errno := ioctl(l.fd(), cecGPhysAddr, &phys); errno != 0 {
		l.log.Warn("cec_failed", "stage", "phys_addr", "errno", int(errno))
	}
	var la cecLogAddrs
	if _, _, errno := ioctl(l.fd(), cecGLogAddrs, &la); errno != 0 {
		l.log.Warn("cec_failed", "stage", "log_addrs", "errno", int(errno))
		return
	}
	if needsClaim(la) {
		la = cecLogAddrs{
			cecVersion: cecVersion1_4, vendorID: 0xffffffff, numLogAddrs: 1,
			logAddrType: [4]uint8{logAddrTypePlay}, primaryDeviceType: [4]uint8{typePlay},
		}
		copy(la.osdName[:], "cadence")
		if _, _, errno := ioctl(l.fd(), cecSLogAddrs, &la); errno != 0 {
			l.log.Warn("cec_failed", "stage", "claim", "errno", int(errno))
		} else {
			l.log.Info("cec_claimed")
		}
		if _, _, errno := ioctl(l.fd(), cecGLogAddrs, &la); errno != 0 {
			return
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.adapter = Adapter{Present: true, DeviceType: "playback", LogicalAddress: lowest(la.logAddrMask)}
	if phys != physAddrInvalid {
		l.adapter.PhysicalAddress = FormatPhysical(phys)
	}
}

// needsClaim: nobody asked the kernel for an address yet. An empty mask alone is not that: with no hdmi sink (no
// hotplug, physical address f.f.f.f) the kernel keeps a claim unallocated until a sink appears and then allocates it
// itself, so claiming again on every reread (once a minute) only repeated cec_claimed while nothing was plugged in
func needsClaim(la cecLogAddrs) bool { return la.numLogAddrs == 0 }

// lowest is the mask's lowest claimed address, -1 for none
func lowest(mask uint16) int {
	for la := 0; la <= 15; la++ {
		if mask&(1<<la) != 0 {
			return la
		}
	}
	return -1
}

// receive drains the wire's frames to the sinks, one ioctl at a time; every 60th timeout it rereads the
// adapter (a replug moves its address)
func (l *Linux) receive(ctx context.Context) {
	timeouts := 0
	for ctx.Err() == nil {
		var m cecMsg
		m.timeout = 1000
		_, _, errno := ioctl(l.fd(), cecReceive, &m)
		if errno == syscall.EINTR {
			continue
		}
		if errno == syscall.ETIMEDOUT {
			timeouts++
			if timeouts%60 == 0 {
				l.readAdapter()
			}
			continue
		}
		if errno != 0 {
			if ctx.Err() == nil {
				l.log.Warn("cec_failed", "stage", "receive", "errno", int(errno))
			}
			return
		}
		n := min(int(m.len), len(m.msg))
		if f, err := Parse(m.msg[:n]); err == nil {
			l.sinks.deliver(f)
		}
	}
}

// Send transmits msg with the adapter's initiator nibble and, with want set, waits for that reply (the
// ioctl's own timeout bounds the wait; ctx ends it before it starts). ok false is a nack, a lost reply or
// a wire already closed.
func (l *Linux) Send(ctx context.Context, msg Frame, want byte) (Frame, bool) {
	if err := ctx.Err(); err != nil {
		return Frame{}, false
	}
	l.mu.Lock()
	f, ready, la := l.f, l.ready, l.adapter.LogicalAddress
	l.mu.Unlock()
	if !ready || f == nil {
		return Frame{}, false
	}
	if la < 0 || la > 15 {
		la = 15 // unregistered: the wire's initiator for an adapter that claimed nothing
	}
	msg.Init = byte(la)
	out, errno := l.xmit(f, msg, want)
	if errno == syscall.ENONET {
		// the adapter is unconfigured (a tv in standby that dropped its hotplug): the wire allows exactly
		// one thing then, the wake from unregistered to the tv, so a view on still goes out
		if msg.Dest != 0 || (!msg.Polling && msg.Opcode != OpTextViewOn && msg.Opcode != OpImageViewOn) {
			return Frame{}, false
		}
		msg.Init = 0xf
		out, errno = l.xmit(f, msg, want)
	}
	if errno != 0 && errno != syscall.EINTR {
		return Frame{}, false
	}
	if out.txStatus&cecTxStatusOK == 0 {
		return Frame{}, false
	}
	if want == 0 {
		return Frame{}, true
	}
	if out.rxStatus&cecRxStatusOK == 0 || out.len < 1 || out.len > 16 {
		return Frame{}, false
	}
	reply, err := Parse(out.msg[:out.len])
	if err != nil {
		return Frame{}, false
	}
	return reply, true
}

// xmit is one CEC_TRANSMIT with its reply wait
func (l *Linux) xmit(f *os.File, msg Frame, want byte) (cecMsg, syscall.Errno) {
	out := cecMsg{timeout: 1000}
	b := msg.Bytes()
	out.len = uint32(min(len(b), len(out.msg)))
	copy(out.msg[:], b)
	if want != 0 {
		out.reply = want
	}
	_, _, errno := ioctl(f.Fd(), cecTransmit, &out)
	return out, errno
}

// fd is the open file's descriptor, 0 for none (ioctls on it then fail harmlessly)
func (l *Linux) fd() uintptr {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0
	}
	return l.f.Fd()
}

func ioctl[T any](fd, req uintptr, arg *T) (uintptr, uintptr, syscall.Errno) {
	return syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(arg))) //nolint:gosec // the ioctl takes its argument by address
}

func errnoOf(err error) int {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return int(errno)
	}
	return 0
}

var _ Bus = (*Linux)(nil)
