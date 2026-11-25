//go:build !linux

package audiod

import "syscall"

// audiod runs on linux only; this keeps the package building on dev hosts
func procAttr() *syscall.SysProcAttr { return nil }
