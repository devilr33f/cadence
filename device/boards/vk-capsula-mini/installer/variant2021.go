package installer

import (
	"slices"

	"git.shino.boo/devilreef/cadence/device/internal/install"
)

// firmware2021 is the capsula's older firmware as probed on 2026-11-20 (Buildroot 2019.11.1, a kernel of 2021-12-29
// built with gcc 8.3), jailbroken by capsula-jb: root over adb, whose adbd its overlay S99usbgadget.sh starts. its
// stock wakes on a msgpack model (no model.bin), runs no stereoctl, and its fwupdater hands images to the swupdate
// binary (no swupdate.sh), so that is the file stubbed
func firmware2021() install.Variant {
	return install.Variant{
		Name: "2021 (Buildroot 2019.11.1)",
		Facts: []install.Fact{
			{Name: "kernel", Paths: []string{"/proc/sys/kernel/osrelease"}, Want: "4.9.113"},
			{Name: "build", Paths: []string{"/proc/version"}, Want: "Linux version 4.9.113 (root@runner-dozgq8sf-project-13986-concurrent-0) " +
				"(gcc version 8.3.0 (GNU Toolchain for the A-profile Architecture 8.3-2019.03 (arm-rel-8.36)) ) " +
				"#1 SMP PREEMPT Wed Dec 29 16:01:32 UTC 2021"},
			{Name: "hwrevision", Paths: []string{"/etc/hwrevision"}, Want: "S4xx 1.0"},
			{Name: "os-release", Paths: []string{"/etc/os-release"}, Want: "NAME=Buildroot\nVERSION=2019.11.1-g7f5d8e9e6\nID=buildroot\n" +
				"VERSION_ID=2019.11.1\nPRETTY_NAME=\"Buildroot 2019.11.1\""},
		},
		Apply: func(p *install.Profile) {
			p.RecoveryDaemons = []string{"adbd"}
			st := *p.Stock
			st.Files = []string{"/usr/bin/voice-agent", stock + "/model.msgpack"}
			p.Stock = &st
			p.PlatformScripts = slices.DeleteFunc(slices.Clone(p.PlatformScripts),
				func(s install.PlatformScript) bool { return s.Process == "stereoctl" })
			p.Hardening = []install.HardeningStep{
				&install.OverlayStub{StepName: "fwupdater-stub", Path: "/usr/bin/fwupdater", Lower: rom + "/usr/bin/fwupdater", Content: fwupdaterStub},
				&install.OverlayStub{StepName: "swupdate-bin-stub", Path: "/usr/bin/swupdate", Lower: rom + "/usr/bin/swupdate", Content: swupdateStub},
			}
		},
	}
}
