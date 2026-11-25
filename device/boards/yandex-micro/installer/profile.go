// Package installer describes the experimentally supported Station Micro.
package installer

import "git.shino.boo/devilreef/cadence/device/internal/install"

const (
	vendor = "/system/vendor/quasar"
	getty  = "console::respawn:/sbin/getty_rabbit_hole -L  console 0 vt100 # GENERIC_SERIAL"
)

func Profile() install.Profile {
	return install.Profile{
		Board: "yandex-micro", Arch: "armhf", DataRoot: "/data/cadence", DataMount: "/data",
		Inittab: "/etc/inittab", ModelPath: "/proc/device-tree/model", Model: "Yandex Station Micro v1.2",
		KernelPath: "/proc/sys/kernel/osrelease", KernelFamily: "4.9.",
		Facts: []install.Fact{
			{Name: "kernel", Paths: []string{"/proc/sys/kernel/osrelease"}, Want: "4.9.68"},
			{Name: "VERSION", Paths: []string{vendor + "/VERSION"}, Want: "3.21"},
			{Name: "version_full", Paths: []string{vendor + "/version_full"}, Want: "0.369.1.48.4790768027.20261019.297"},
		},
		// 0.369.1.48's onboot.sh starts adbd from the two persistent markers;
		// the transport probe and running check still guard every inittab write.
		Recovery: nil, RecoveryDaemons: []string{"adbd"}, Anchor: getty,
		InittabForbidden: []string{"/system/vendor/"},
		Stock: &install.Stock{Dir: vendor, Start: vendor + "/run.sh", Launcher: vendor + "/quasar_launcher2",
			Files: []string{vendor + "/libs/libyandex_evil.so"}},
		PlatformDaemons: []install.Daemon{
			{Exe: "/usr/bin/bluealsa-aplay", Args: []string{"-M", "disable", "-S"}},
			{Exe: "/usr/bin/just-works-bluez-agent"},
		},
		RequireOTABlocked: true,
		Hardening: []install.HardeningStep{
			install.Uninspected("ota-key"), install.Uninspected("ota-script-stub"), install.Uninspected("updatesd-launch"),
		},
		MixerControl: "Master", SpareKB: 16 << 10,
		Tools: []string{
			"amixer", "awk", "cat", "chmod", "cmp", "cp", "cut", "date", "df", "grep", "head", "kill", "ls", "mkdir",
			"mount", "mv", "od", "pidof", "readlink", "rm", "sed", "sh", "sha256sum", "sleep", "start-stop-daemon", "sync",
			"tar", "tr", "wc",
		},
		MinRootKB: 2048,
	}
}
