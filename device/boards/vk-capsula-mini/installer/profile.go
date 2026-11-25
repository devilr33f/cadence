// Package installer is what cadence-install knows of the VK Capsula Mini: its firmware, capsula-root's recovery
// access, the stock assistant it keeps, the overlay launcher that boots cadence in place of the stock's init script,
// and the OTA stubs. The boot wrapper's capsula part, board-hook, is in ../files.
package installer

import (
	_ "embed"

	"git.shino.boo/devilreef/cadence/device/internal/install"
)

const (
	// the lower layer, where the stock's own copies stay readable under the overlay
	rom = "/rom"
	// the stock assistant's init script, which rcS runs by this name at boot
	s97 = "/etc/init.d/S97voice-agent.sh"
	// the stock assistant's data: its sounds and its wake model
	stock = "/usr/share/voice-agent"
)

// launcher boots cadence-run from the stock's init script, or the stock when cadence is missing or disabled
//
//go:embed files/S97voice-agent.sh
var launcher []byte

// Profile is the capsula as probed on 2026-11-18: firmware 44.90 (Buildroot 2022.11.1, kernel 4.9.113), / an rw
// overlay on /data, root over capsula-root's password ssh (its supervisor and dropbearmulti), and the stock assistant
// installed, started by its S97, holding the hardware watchdog.
func Profile() install.Profile {
	return install.Profile{
		Board: "vk-capsula-mini", Arch: "arm64",
		DataRoot: "/data/cadence", DataMount: "/data",
		ModelPath: "/proc/device-tree/compatible", ModelPrefix: "amlogic, axg",
		KernelPath: "/proc/sys/kernel/osrelease", KernelFamily: "4.9.",
		// the firmware's own version is compiled into devinfo and no fwlabel file exists, so the build is pinned by
		// its kernel and its buildroot release
		Facts: []install.Fact{
			{Name: "kernel", Paths: []string{"/proc/sys/kernel/osrelease"}, Want: "4.9.113"},
			{Name: "build", Paths: []string{"/proc/version"}, Want: "Linux version 4.9.113 (root@gitlab-t5.go.devmail.ru) " +
				"(gcc version 10.3.1 20210621 (GNU Toolchain for the A-profile Architecture 10.3-2021.07 (arm-10.29)) ) " +
				"#1 SMP PREEMPT Mon Mar 3 15:20:22 UTC 2025"},
			{Name: "hwrevision", Paths: []string{"/etc/hwrevision"}, Want: "S4xx 1.0"},
			{Name: "os-release", Paths: []string{"/etc/os-release"}, Want: "NAME=Buildroot\nVERSION=-gc335d380a\nID=buildroot\n" +
				"VERSION_ID=2022.11.1\nPRETTY_NAME=\"Buildroot 2022.11.1\""},
		},
		Variants: []install.Variant{firmware2021()},
		// capsula-root's: started from its overlay S89usbgadget.sh, never init's table
		RecoveryDaemons: []string{"dropbearmulti", "supervisor"},
		Stock: &install.Stock{
			Dir: stock, Start: rom + s97, Launcher: "/usr/bin/voice-agent",
			Files: []string{"/usr/bin/voice-agent", stock + "/model.bin"},
		},
		OverlayRoot: true, InitScript: s97, InitScriptLower: rom + s97, InitLauncher: launcher,
		InitPidFile: "/run/cadence/cadence-run.pid",
		BootPath:    "/usr/sbin:/usr/bin:/sbin:/bin",
		// the hook stops these while cadence runs: okcall's own copy runs as /tmp/okcall, and the volume syncer would
		// rewrite TV-Set at every Pulse sink event
		PlatformScripts: []install.PlatformScript{
			{Script: "/etc/init.d/S97okcall.sh", Process: "okcall"},
			{Script: "/etc/init.d/S96stereoctl.sh", Process: "stereoctl"},
			{Script: "/etc/init.d/S99sysmon.sh", Process: "sysmon"},
			{Script: "/etc/init.d/S51ntp-ibb-volume-syncer", Process: "ntp-ibb-volume-syncer"},
		},
		Hardening: hardening(),
		// TV-Set's raw value is the level, 0 to 100; Master stays at its stock 255
		MixerControl: "TV-Set",
		// every program cadence-install's commands and cadence-run's modes run over capsula-root's ssh session, whose
		// PATH has capsula-root's busybox first (tools_test.go keeps it so)
		Tools: []string{
			"amixer", "awk", "cat", "chmod", "cp", "date", "dd", "df", "du", "grep", "hexdump", "kill", "ls", "mkdir", "mv",
			"pidof", "readlink", "rm", "sh", "sha256sum", "sleep", "start-stop-daemon", "tar", "tr", "wc",
		},
		// /data holds the tree and the overlay's upper layer; the tarball waits in /tmp, a tmpfs
		HexRead: true, StageDir: "/tmp/cadence-install", SpareKB: 16 << 10,
		RefuseIfPresent: []install.Refusal{{
			Path:   "/data/update_successful",
			Reason: "a firmware update is being checked: S98selfcheck.sh switches to the other slot and reboots once voice-agent is not running",
		}},
	}
}
