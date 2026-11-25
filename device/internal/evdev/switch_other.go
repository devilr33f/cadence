//go:build !linux

package evdev

import "os"

// Switch cannot tell off linux, where there is no evdev
func Switch(*os.File, uint16) (on, ok bool) { return false, false }
