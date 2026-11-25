package installer_test

import (
	"slices"
	"testing"

	"git.shino.boo/devilreef/cadence/device/boards/vk-capsula-mini/installer"
	"git.shino.boo/devilreef/cadence/device/internal/install"
)

// the 2021 firmware as probed on 2026-11-20: its own facts, recovery through capsula-jb's adbd, the stock's msgpack
// model, no stereoctl, and the swupdate binary stubbed in place of swupdate.sh; the 44.90 profile is left as it was
func TestVariant2021(t *testing.T) {
	base := installer.Profile()
	if len(base.Variants) != 1 {
		t.Fatalf("variants %+v", base.Variants)
	}
	v := base.Variants[0]
	facts := map[string]string{}
	for _, f := range v.Facts {
		facts[f.Name] = f.Want
	}
	if facts["kernel"] != "4.9.113" || facts["hwrevision"] != "S4xx 1.0" ||
		facts["build"] != "Linux version 4.9.113 (root@runner-dozgq8sf-project-13986-concurrent-0) (gcc version 8.3.0 (GNU Toolchain for the A-profile Architecture 8.3-2019.03 (arm-rel-8.36)) ) #1 SMP PREEMPT Wed Dec 29 16:01:32 UTC 2021" ||
		facts["os-release"] != "NAME=Buildroot\nVERSION=2019.11.1-g7f5d8e9e6\nID=buildroot\nVERSION_ID=2019.11.1\nPRETTY_NAME=\"Buildroot 2019.11.1\"" {
		t.Fatalf("facts %q", facts)
	}
	p := installer.Profile()
	v.Apply(&p)
	if !slices.Equal(p.RecoveryDaemons, []string{"adbd"}) ||
		!slices.Equal(p.Stock.Files, []string{"/usr/bin/voice-agent", "/usr/share/voice-agent/model.msgpack"}) {
		t.Fatalf("recovery %q, stock %+v", p.RecoveryDaemons, p.Stock)
	}
	if slices.ContainsFunc(p.PlatformScripts, func(s install.PlatformScript) bool { return s.Process == "stereoctl" }) || len(p.PlatformScripts) != 3 {
		t.Fatalf("platform scripts %+v", p.PlatformScripts)
	}
	var paths []string
	for _, h := range p.Hardening {
		s := h.(*install.OverlayStub)
		paths = append(paths, s.Path+" "+s.Lower)
	}
	if !slices.Equal(paths, []string{"/usr/bin/fwupdater /rom/usr/bin/fwupdater", "/usr/bin/swupdate /rom/usr/bin/swupdate"}) {
		t.Fatalf("hardening %q", paths)
	}
	if !slices.Equal(base.RecoveryDaemons, []string{"dropbearmulti", "supervisor"}) || len(base.PlatformScripts) != 4 ||
		base.Stock.Files[1] != "/usr/share/voice-agent/model.bin" {
		t.Fatal("applying the variant changed the 44.90 profile")
	}
}
