//go:build linux

package discovery

// SO_REUSEPORT, which the frozen syscall package lacks on linux: 15 in asm-generic/socket.h, on every arch cadence
// builds for (arm, arm64, amd64, 386; android too), not on mips, sparc or parisc
const soReusePort = 0xf
