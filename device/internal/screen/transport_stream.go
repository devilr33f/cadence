//go:build !linux && !android

package screen

// a host without SOCK_SEQPACKET (macos, windows) frames a stream: development and tests only
const network = "unix"
