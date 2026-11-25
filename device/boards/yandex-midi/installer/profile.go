// Package installer is what cadence-install knows of the midi: its firmware, recovery entries, the stock assistant it
// keeps, its hardening steps and its mixer. The boot wrapper's midi part, board-hook, is in ../files; the tarball ships
// it as share/board-hook.
package installer

import "git.shino.boo/devilreef/cadence/device/internal/install"

const (
	adbd = "::respawn:/usr/bin/adbd"
	// the stock assistant's directory, which a midi keeps (M2m-R2)
	vendor = "/system/vendor/quasar"
)

// hardening are S0 §7.3's steps for a midi's OTA (M2m-D3): the key the stock updater checks an update with swapped,
// device_ota_update.sh replaced by a stub that changes nothing, and updatesd out of the launcher's launch.json. Their
// files have not been inspected on a midi, so none is enabled: the router's OTA block stands in, and every install
// needs --ota-blocked
var hardening = []install.HardeningStep{
	install.Uninspected("ota-key"), install.Uninspected("ota-script-stub"), install.Uninspected("updatesd-launch"),
}

// Profile is the midi as v1 inspected it (2026-10-28): firmware 3.21 on kernel 4.9.68, adbd and dropbear in init as
// v1's provisioning put them, the stock assistant installed and started by its own init script. The data root is the
// board's (paths.go).
func Profile() install.Profile {
	return install.Profile{
		Board: "yandex-midi", Arch: "armhf",
		DataRoot: "/data/cadence", DataMount: "/data",
		Inittab:   "/etc/inittab",
		ModelPath: "/proc/device-tree/model", Model: "Yandex Station Midi",
		KernelPath: "/proc/sys/kernel/osrelease", KernelFamily: "4.9.",
		// v1's fingerprint's names and values; its hashes of the stock files are not carried (rule 5)
		Facts: []install.Fact{
			{Name: "kernel", Paths: []string{"/proc/sys/kernel/osrelease"}, Want: "4.9.68"},
			{Name: "VERSION", Paths: []string{vendor + "/VERSION"}, Want: "3.21"},
			{Name: "version_full", Paths: []string{vendor + "/version_full"}, Want: "0.369.1.46.4793639783.20261020.295"},
		},
		Recovery: []string{
			adbd, "::respawn:/usr/sbin/dropbear -F -p 22 -r /etc/dropbear/dropbear_ed25519_host_key",
		},
		RecoveryDaemons:  []string{"adbd", "dropbear"},
		Anchor:           adbd,
		InittabForbidden: []string{"/system/vendor/"},
		Stock: &install.Stock{
			Dir: vendor, Start: vendor + "/run.sh", Launcher: vendor + "/quasar_launcher2",
			Files: []string{vendor + "/libs/libyandex_evil.so"},
		},
		// the hook stops these two while cadence runs (cadenced runs the adapter, with its own agent): bluealsa-aplay
		// as S53 runs it; S54 runs the agent, whose arguments, if any, have not been read
		PlatformDaemons: []install.Daemon{
			{Exe: "/usr/bin/bluealsa-aplay", Args: []string{"-M", "disable", "-S"}},
			{Exe: "/usr/bin/just-works-bluez-agent"},
		},
		RequireOTABlocked: true,
		Hardening:         hardening,
		MixerControl:      "Master",
		// every program cadence-install's commands, swap-inittab.sh, cadence-run and midi's hook run (tools_test.go
		// keeps it so)
		Tools: []string{
			"amixer", "awk", "cat", "chmod", "cmp", "cp", "cut", "date", "df", "grep", "head", "kill", "ls", "mkdir",
			"mount", "mv", "od", "pidof", "readlink", "rm", "sed", "sh", "sha256sum", "sleep", "start-stop-daemon", "sync",
			"tar", "tr", "wc",
		},
		MinRootKB: 2048,
	}
}
