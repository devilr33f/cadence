package installer_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/boards/yandex-midi/installer"
)

// programs a busybox userland may lack; preflight checks every one the installer's commands, the boot wrapper and
// midi's hook run
var applets = []string{
	"amixer", "awk", "basename", "cat", "chmod", "chown", "cmp", "cp", "cut", "date", "dd", "df", "dirname", "du",
	"find", "grep", "gzip", "head", "kill", "ln", "ls", "md5sum", "mkdir", "mknod", "mktemp", "mount", "mv", "nohup",
	"od", "pidof", "pkill", "readlink", "realpath", "rm", "rmdir", "sed", "setsid", "sh", "sha256sum", "sleep",
	"sort", "start-stop-daemon", "stat", "sync", "tail", "tar", "tee", "touch", "tr", "umount", "uname", "uniq", "wc",
	"xargs",
}

var (
	shellComment = regexp.MustCompile(`(?m)(^|\s)#.*$`)
	goString     = regexp.MustCompile("`[^`]*`|\"(?:[^\"\\\n]|\\.)*\"")
)

// the code the device runs: the boot wrapper with midi's hook, the inittab swap, and the string literals the Shell
// builds its commands from
func deviceCode(t *testing.T) string {
	t.Helper()
	var code strings.Builder
	for _, f := range []string{
		"../../../internal/install/files/cadence-run", "../files/board-hook", "../../../internal/install/swap-inittab.sh",
	} {
		code.WriteString(shellComment.ReplaceAllString(read(t, f), "$1"))
	}
	for _, s := range goString.FindAllString(read(t, "../../../internal/install/shell.go"), -1) {
		code.WriteString(s + "\n")
	}
	return code.String()
}

// a program the scripts run that preflight does not check would fail mid-swap, root writable or the stock half
// suspended
func TestToolsCoverTheScripts(t *testing.T) {
	code, tools := deviceCode(t), installer.Profile().Tools
	for _, a := range applets {
		if regexp.MustCompile(`(^|[^\w.-])`+regexp.QuoteMeta(a)+`($|[^\w.-])`).MatchString(code) && !slices.Contains(tools, a) {
			t.Errorf("the scripts run %s, which preflight does not check", a)
		}
	}
}
