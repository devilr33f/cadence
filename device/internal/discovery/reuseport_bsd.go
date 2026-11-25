//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package discovery

import "syscall"

const soReusePort = syscall.SO_REUSEPORT
