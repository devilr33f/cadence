package androidtv

import (
	"errors"
	"io"
	"io/fs"
	"os/exec"
	"syscall"
)

// soloist is built for arm64 alone, and the stick's userland is armv7, so the arm64 build runs beside the 32-bit
// app as on midi — but only if the kernel is 64-bit, and a 32-bit process cannot take uname's word for it: the
// compat personality renames the machine (aarch64 reads as armv8l to the 32-bit shell and to cadenced alike). so
// the kernel is asked four ways, each trusted only for a yes, and only one can say no:
//
//	uname              as this process's personality shows it (a 64-bit process alone sees aarch64)
//	personality        uname again with PER_LINUX32 cleared, which the 64-bit kernel answers aarch64
//	arch               /proc/sys/kernel/arch, the init namespace's machine, whatever asks
//	probe              the runtime's own aarch64 loader exec'd with --version: ENOEXEC is the kernel's own
//	                   refusal, and a loader that answers at all proves the kernel execs aarch64
//
// cadenced logs which one answered (soloist_kernel how=…); a kernel that cannot run soloist leaves the capability
// away with soloist_kernel_refused, and one nothing could answer is logged as unknown rather than refused.

// the machine the app names instead of asking the kernel itself, both ways: aarch64 runs, anything else does not
const envKernelMachine = "CADENCE_ANDROID_KERNEL_MACHINE"

// the runtime's aarch64 loader, where the assembler puts it (deploy/builder/assemble-soloist-runtime.sh), and where
// this board's own soloist runtime lands once the app unpacks it
const arm64Loader = "lib/ld-linux-aarch64.so.1"

// probeAnswer is what exec'ing the runtime's aarch64 loader said
type probeAnswer int

const (
	// not asked: no runtime to probe
	probeNone probeAnswer = iota
	// it ran, whatever it exited: the kernel execs aarch64
	probeRan
	// ENOEXEC: the kernel runs no 64-bit arm at all
	probeRefused
)

// kernelSignals is the kernel as each way of asking sees it
type kernelSignals struct {
	uname            string
	personalityUname string
	sysctlArch       string
	probe            probeAnswer
}

// kernelArm64 says whether the kernel runs arm64 builds, and which question answered. a machine string can be
// renamed for a compat process, so only the probe's refusal is a no; nothing answering is an unknown, not a
// 32-bit verdict
func kernelArm64(s kernelSignals) (bool, string) {
	switch {
	case s.uname == "aarch64":
		return true, "uname"
	case s.personalityUname == "aarch64":
		return true, "personality"
	case s.sysctlArch == "aarch64":
		return true, "arch"
	case s.probe == probeRan:
		return true, "probe"
	case s.probe == probeRefused:
		return false, "refused"
	default:
		return false, "unknown"
	}
}

// probeArm64 execs the loader: a process that answers --version at all proves the kernel execs aarch64, whatever it
// exited; ENOEXEC is the kernel's own refusal; anything else — missing, without the exec bit, stopped by the
// sandbox — says nothing of the kernel's arch
func probeArm64(loader string) probeAnswer {
	if loader == "" {
		return probeNone
	}
	cmd := exec.Command(loader, "--version")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if err == nil {
		return probeRan
	}
	// a start that never happened is a *fs.PathError around the fork/exec (an explicit path) or an *exec.Error (a
	// name the path lookup refused); the kernel's errno is in either
	var failed *fs.PathError
	var missing *exec.Error
	if errors.As(err, &failed) || errors.As(err, &missing) {
		cause := failed.Err
		if cause == nil && missing != nil {
			cause = missing.Err
		}
		if errors.Is(cause, syscall.ENOEXEC) {
			return probeRefused
		}
		return probeNone
	}
	return probeRan
}

// kernelRunsArm64 is the kernel's answer for this board: the app's word when it has one, else every question the
// process can ask. loader is the runtime's aarch64 loader to probe, "" without a runtime
func kernelRunsArm64(word, loader string) (bool, string) {
	if word != "" {
		return word == "aarch64", "env"
	}
	return kernelArm64(kernelSignals{
		uname:            unameMachine(),
		personalityUname: personalityClearedUname(),
		sysctlArch:       sysctlArch(),
		probe:            probeArm64(loader),
	})
}
