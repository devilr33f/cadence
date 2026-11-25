package installer_test

import (
	"regexp"
	"slices"
	"testing"

	"git.shino.boo/devilreef/cadence/device/boards/vk-capsula-mini/installer"
	"git.shino.boo/devilreef/cadence/device/internal/install/busyboxtest"
)

// programs a busybox userland may lack, with those the capsula's stock busybox lacks at boot (sed, awk, od, cmp, id,
// stat, timeout, flock, setsid)
var applets = []string{
	"amixer", "awk", "basename", "cat", "chmod", "chown", "cmp", "cp", "cut", "date", "dd", "df", "dirname", "du",
	"expr", "find", "flock", "grep", "gzip", "head", "hexdump", "id", "kill", "killall", "ln", "logger", "ls", "md5sum",
	"mkdir", "mkfifo", "mknod", "mktemp", "mount", "mv", "nohup", "od", "pgrep", "pidof", "pkill", "readlink",
	"realpath", "rm", "rmdir", "sed", "seq", "setsid", "sh", "sha256sum", "sleep", "sort", "start-stop-daemon", "stat",
	"sync", "tail", "tar", "tee", "timeout", "touch", "tr", "umount", "uname", "uniq", "usleep", "wc", "xargs",
}

var (
	shellComment = regexp.MustCompile(`(?m)(^|\s)#.*$`)
	goString     = regexp.MustCompile("`[^`]*`|\"(?:[^\"\\\n]|\\.)*\"")
	headBytes    = regexp.MustCompile(`(^|[^\w.-])head\s+(-[a-z]*c|-[0-9]|--bytes)`)
)

func code(t *testing.T, path string) string {
	t.Helper()
	return shellComment.ReplaceAllString(read(t, path), "$1")
}

func runs(code, applet string) bool {
	return regexp.MustCompile(`(^|[^\w.-])` + regexp.QuoteMeta(applet) + `($|[^\w.-])`).MatchString(code)
}

// unused are programs the shared commands name only on paths this board never takes: Read's head and od (it reads
// with dd and hexdump, HexRead), and the inittab swap's mount (its root is an overlay, never remounted)
func unused(t *testing.T) map[string]bool {
	t.Helper()
	p := installer.Profile()
	if !p.HexRead || !p.OverlayRoot {
		t.Fatal("the shared commands' head, od and mount are this board's again")
	}
	return map[string]bool{"head": true, "od": true, "mount": true}
}

// a program the installer's own commands run over capsula-root's ssh session (the string literals they are built
// from, the push's cat, and cadence-run's modes the session runs, with the hook they source) that preflight does not
// check would fail mid-install
func TestToolsCoverTheScripts(t *testing.T) {
	var all string
	for _, f := range []string{"shell.go", "read_hex.go", "initscript.go", "ssh.go", "usage.go"} {
		for _, s := range goString.FindAllString(read(t, "../../../internal/install/"+f), -1) {
			all += s + "\n"
		}
	}
	all += code(t, "../../../internal/install/files/cadence-run") + code(t, "../files/board-hook")
	tools, skip := installer.Profile().Tools, unused(t)
	for _, a := range applets {
		if runs(all, a) && !skip[a] && !slices.Contains(tools, a) {
			t.Errorf("the installer runs %s, which preflight does not check", a)
		}
	}
	for _, a := range tools {
		if skip[a] {
			t.Errorf("preflight checks %s, which this board never runs", a)
		}
	}
}

// the scripts init, voice-agent or the hook run at boot see init's PATH and the stock busybox alone: no sed, awk, od,
// cmp or head -c there, and capsula-root's tools are only on its ssh session's PATH. the launcher is sourced by rcS
// and rcK, so it never exits their shell
func TestBootScriptsUseStockApplets(t *testing.T) {
	scripts := []string{
		"../../../internal/install/files/cadence-run", "files/S97voice-agent.sh", "files/fwupdater.stub",
		"files/swupdate.sh.stub", "../files/board-hook",
	}
	for _, s := range scripts {
		t.Run(s, func(t *testing.T) {
			c := code(t, s)
			for _, a := range applets {
				if runs(c, a) && !slices.Contains(busyboxtest.CapsulaBootApplets, a) {
					t.Errorf("%s runs %s, which the stock busybox lacks on init's PATH", s, a)
				}
			}
			if headBytes.MatchString(c) {
				t.Errorf("%s runs head with a byte count, which the stock busybox lacks", s)
			}
		})
	}
	if runs(code(t, "files/S97voice-agent.sh"), "exit") {
		t.Error("the launcher exits, which would end rcS's shell")
	}
}
