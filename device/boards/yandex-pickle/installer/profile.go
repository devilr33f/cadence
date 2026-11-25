// Package installer is what cadence-install knows of the pickle: its firmware, recovery entries, vendor files and
// mixer. cadence-run, the boot wrapper init runs, is in ../files; the tarball ships it as share/cadence-run.
package installer

import "git.shino.boo/devilreef/cadence/device/internal/install"

// stockRoots are where v1's provisioning moved the vendor's version files, else the vendor dirs (rule 4 requires
// those gone)
var stockRoots = []string{"/usr/lib/cadence-stock", "/system/vendor/quasar", "/system/vendor/.quasar-disabled"}

func inStock(name string) []string {
	out := make([]string, len(stockRoots))
	for i, r := range stockRoots {
		out[i] = r + "/" + name
	}
	return out
}

const adbd = "::respawn:/usr/bin/adbd"

// Profile is the pickle as v1 provisioned it (2026-10-25): firmware 3.21 on kernel 5.4.242, adbd and dropbear in
// init, Quasar and its updater deleted. The data root is the board's (paths.go).
func Profile() install.Profile {
	return install.Profile{
		Board: "yandex-pickle", Arch: "arm64",
		DataRoot: "/data/cadence", DataMount: "/data",
		Inittab:   "/etc/inittab",
		ModelPath: "/proc/device-tree/model", ModelPrefix: "Yandex Pickle",
		KernelPath: "/proc/sys/kernel/osrelease", KernelFamily: "5.4.",
		Facts: []install.Fact{
			{Name: "kernel", Paths: []string{"/proc/sys/kernel/osrelease"}, Want: "5.4.242"},
			{Name: "VERSION", Paths: inStock("VERSION"), Want: "3.21"},
			{Name: "version_full", Paths: inStock("version_full"), Want: "0.12.1.22.3114898626.20250530.factory-12"},
		},
		Recovery: []string{
			adbd, "::respawn:/usr/sbin/dropbear -F -p 22 -r /etc/dropbear/dropbear_ed25519_host_key",
		},
		RecoveryDaemons: []string{"adbd", "dropbear"},
		Anchor:          adbd,
		Vendor: []string{
			"/system/vendor/quasar", "/system/vendor/.quasar-disabled", "/etc/init.d/S99quasar", "/etc/init.d/S90clean-ota",
			"/usr/bin/yandex-apply-ota", "/usr/bin/yandex-ota-decrypt", "/usr/bin/yandex-control",
		},
		VendorDaemons:    []string{"maind", "quasar_launcher2"},
		InittabForbidden: []string{"/system/vendor/"},
		MixerControl:     "Master",
		// every program cadence-install's commands, swap-inittab.sh and cadence-run run (tools_test.go keeps it so)
		Tools: []string{
			"amixer", "awk", "cat", "chmod", "cmp", "cp", "cut", "date", "df", "grep", "head", "kill", "ls", "mkdir",
			"mount", "mv", "od", "pidof", "readlink", "rm", "sed", "sh", "sha256sum", "sleep", "sync", "tar", "tr", "wc",
		},
		MinRootKB: 2048,
	}
}
