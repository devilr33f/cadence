//go:build linux || android

package androidtv

import (
	"os"
	"runtime"
	"strings"
	"syscall"
)

// unameMachine is uname's machine as this process's personality shows it: aarch64 to a 64-bit process, armv8l to a
// compat one on a 64-bit kernel, armv7l on a kernel with no 64-bit arm
func unameMachine() string {
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err != nil {
		return ""
	}
	return chars(uts.Machine[:])
}

// the compat personality a 64-bit kernel gives a 32-bit process, which renames the machine
const perLinux32 = 0x8

// personalityClearedUname is uname's machine with PER_LINUX32 cleared: the 64-bit kernel answers aarch64 where the
// same uname read armv8l. personality(2) is per thread and a sandbox may refuse to change it, so "" whenever the
// dance cannot run, and the caller trusts it only for a yes
func personalityClearedUname() string {
	const query = ^uintptr(0) // personality(0xffffffff) reads the current one without changing it
	current, _, errno := syscall.Syscall(syscall.SYS_PERSONALITY, query, 0, 0)
	if errno != 0 {
		return ""
	}
	old := uintptr(current)
	if old&0xff != perLinux32 {
		// no compat personality: uname already says the machine as it is
		return ""
	}
	// the personality and the uname must share one os thread, and the personality goes back as it was
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, _, errno := syscall.Syscall(syscall.SYS_PERSONALITY, 0, 0, 0); errno != 0 {
		return ""
	}
	machine := unameMachine()
	_, _, _ = syscall.Syscall(syscall.SYS_PERSONALITY, old, 0, 0)
	return machine
}

// sysctlArch is /proc/sys/kernel/arch, the init namespace's machine as the kernel names it, whatever personality
// the asking process runs under; "" where the kernel has no such file
func sysctlArch() string {
	out, err := os.ReadFile("/proc/sys/kernel/arch")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// chars is a utsname field as a string, stopped at its first NUL. the field is int8 on 64-bit linux and uint8 on
// 32-bit arm, where cadenced runs here
func chars[E int8 | uint8](field []E) string {
	out := make([]byte, 0, len(field))
	for _, c := range field {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}
