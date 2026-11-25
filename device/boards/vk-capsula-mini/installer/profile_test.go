package installer_test

import (
	"bytes"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/boards/vk-capsula-mini/installer"
	"git.shino.boo/devilreef/cadence/device/internal/install"
)

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // the repo's own files
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// the capsula as probed on 2026-11-18: an rw overlay root booting cadence through the stock's S97, recovery through
// capsula-root's dropbear, the stock kept with its launcher outside its directory, and a busybox without head -c
func TestProfileIsTheProbedCapsula(t *testing.T) {
	p := installer.Profile()
	if p.Board != "vk-capsula-mini" || p.Arch != "arm64" || p.DataRoot != "/data/cadence" || p.DataMount != "/data" {
		t.Fatalf("board %s %s, data %s on %s", p.Board, p.Arch, p.DataRoot, p.DataMount)
	}
	if p.ModelPath != "/proc/device-tree/compatible" || p.ModelPrefix != "amlogic, axg" || p.Model != "" ||
		p.KernelPath != "/proc/sys/kernel/osrelease" || p.KernelFamily != "4.9." {
		t.Fatalf("model %s %q %q, kernel %s %q", p.ModelPath, p.ModelPrefix, p.Model, p.KernelPath, p.KernelFamily)
	}
	if !slices.Equal(p.RecoveryDaemons, []string{"dropbearmulti", "supervisor"}) || p.Recovery != nil || p.Inittab != "" ||
		p.Anchor != "" || p.InittabForbidden != nil {
		t.Fatalf("recovery %q %q, inittab %q %q %q", p.RecoveryDaemons, p.Recovery, p.Inittab, p.Anchor, p.InittabForbidden)
	}
	s := p.Stock
	if s == nil || s.Dir != "/usr/share/voice-agent" || s.Start != "/rom/etc/init.d/S97voice-agent.sh" ||
		s.Launcher != "/usr/bin/voice-agent" || !slices.Equal(s.Files, []string{"/usr/bin/voice-agent", "/usr/share/voice-agent/model.bin"}) {
		t.Fatalf("stock %+v", s)
	}
	if len(p.Vendor) != 0 || len(p.VendorDaemons) != 0 || len(p.PlatformDaemons) != 0 {
		t.Fatalf("vendor %q %q, platform daemons %+v", p.Vendor, p.VendorDaemons, p.PlatformDaemons)
	}
	if !p.OverlayRoot || p.InitScript != "/etc/init.d/S97voice-agent.sh" || p.InitScriptLower != "/rom/etc/init.d/S97voice-agent.sh" ||
		p.InitPidFile != "/run/cadence/cadence-run.pid" || p.BootPath != "/usr/sbin:/usr/bin:/sbin:/bin" {
		t.Fatalf("boot %v %s %s %s %s", p.OverlayRoot, p.InitScript, p.InitScriptLower, p.InitPidFile, p.BootPath)
	}
	if !bytes.Equal(p.InitLauncher, []byte(read(t, "files/S97voice-agent.sh"))) {
		t.Fatal("the launcher is not files/S97voice-agent.sh")
	}
	if !slices.Equal(p.PlatformScripts, []install.PlatformScript{
		{Script: "/etc/init.d/S97okcall.sh", Process: "okcall"},
		{Script: "/etc/init.d/S96stereoctl.sh", Process: "stereoctl"},
		{Script: "/etc/init.d/S99sysmon.sh", Process: "sysmon"},
		{Script: "/etc/init.d/S51ntp-ibb-volume-syncer", Process: "ntp-ibb-volume-syncer"},
	}) {
		t.Fatalf("platform scripts %+v", p.PlatformScripts)
	}
	if p.RequireOTABlocked || !p.HexRead || p.StageDir != "/tmp/cadence-install" || p.SpareKB != 16<<10 || p.MinRootKB != 0 {
		t.Fatalf("ota %v, hex %v, stage %s, spare %d, root %d", p.RequireOTABlocked, p.HexRead, p.StageDir, p.SpareKB, p.MinRootKB)
	}
	// TV-Set's raw value is the level, 0 to 100, as the stock syncer sets it; Master stays at its stock 255
	if p.MixerControl != "TV-Set" {
		t.Fatalf("mixer %q", p.MixerControl)
	}
	if len(p.RefuseIfPresent) != 1 || p.RefuseIfPresent[0].Path != "/data/update_successful" || p.RefuseIfPresent[0].Reason == "" {
		t.Fatalf("refusals %+v", p.RefuseIfPresent)
	}
}

// the firmware read on the probed device, pinned whole: a difference refuses unless the owner accepts it. the
// firmware's own version is compiled into devinfo, and no fwlabel file exists, so neither is a fact
func TestProfileFirmware(t *testing.T) {
	want := map[string]struct{ path, value string }{
		"kernel":     {"/proc/sys/kernel/osrelease", "4.9.113"},
		"build":      {"/proc/version", "Linux version 4.9.113 (root@gitlab-t5.go.devmail.ru) (gcc version 10.3.1 20210621 (GNU Toolchain for the A-profile Architecture 10.3-2021.07 (arm-10.29)) ) #1 SMP PREEMPT Mon Mar 3 15:20:22 UTC 2025"},
		"hwrevision": {"/etc/hwrevision", "S4xx 1.0"},
		"os-release": {"/etc/os-release", "NAME=Buildroot\nVERSION=-gc335d380a\nID=buildroot\nVERSION_ID=2022.11.1\nPRETTY_NAME=\"Buildroot 2022.11.1\""},
	}
	p := installer.Profile()
	if len(p.Facts) != len(want) {
		t.Fatalf("facts %+v", p.Facts)
	}
	hash := regexp.MustCompile(`[0-9a-f]{32,}`)
	for _, f := range p.Facts {
		w, ok := want[f.Name]
		if !ok || !slices.Equal(f.Paths, []string{w.path}) || f.Want != w.value || hash.MatchString(f.Want) {
			t.Errorf("fact %+v", f)
		}
	}
}

// the two OTA steps are inspected and applied: each shadows its vendor file in the overlay with our stub and reverts
// to the lower copy, so no --ota-blocked is needed
func TestProfileHardeningStubsTheOTA(t *testing.T) {
	want := []install.OverlayStub{
		{StepName: "fwupdater-stub", Path: "/usr/bin/fwupdater", Lower: "/rom/usr/bin/fwupdater", Content: []byte(read(t, "files/fwupdater.stub"))},
		{StepName: "swupdate-stub", Path: "/usr/bin/swupdate.sh", Lower: "/rom/usr/bin/swupdate.sh", Content: []byte(read(t, "files/swupdate.sh.stub"))},
	}
	steps := installer.Profile().Hardening
	if len(steps) != len(want) {
		t.Fatalf("steps %d", len(steps))
	}
	for i, s := range steps {
		got, ok := s.(*install.OverlayStub)
		if !ok || got.StepName != want[i].StepName || got.Path != want[i].Path || got.Lower != want[i].Lower ||
			!bytes.Equal(got.Content, want[i].Content) {
			t.Errorf("step %d: %+v", i, s)
		}
	}
}

// the stubs are our own few lines of shell, never a vendor file: each logs once and answers as voice-agent reads "no
// update", exit status 1 (never 0, which voice-agent takes as an update done and reboots for)
func TestProfileStubsAreOurs(t *testing.T) {
	logLine := regexp.MustCompile(`(?m)^logger -t cadence "[a-z :.]+"$`)
	for _, name := range []string{"files/fwupdater.stub", "files/swupdate.sh.stub"} {
		s := read(t, name)
		if len(s) >= 1024 || !strings.HasPrefix(s, "#!/bin/sh\n") || strings.Contains(s, "\x7fELF") || strings.ContainsRune(s, 0) {
			t.Errorf("%s: %d bytes, not a small shell script", name, len(s))
		}
		if n := len(logLine.FindAllString(s, -1)); n != 1 || strings.Count(s, "logger") != 1 {
			t.Errorf("%s logs %d times", name, n)
		}
		if !strings.HasSuffix(s, "\nexit 1\n") {
			t.Errorf("%s does not end with exit 1", name)
		}
	}
}
