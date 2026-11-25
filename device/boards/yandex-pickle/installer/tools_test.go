package installer_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/boards/yandex-pickle/installer"
)

// programs a busybox userland may lack; preflight checks every one the installer's commands and the boot wrapper run
var applets = []string{
	"amixer", "awk", "basename", "cat", "chmod", "chown", "cmp", "cp", "cut", "date", "dd", "df", "dirname", "du",
	"find", "grep", "gzip", "head", "kill", "ln", "ls", "md5sum", "mkdir", "mknod", "mktemp", "mount", "mv", "nohup",
	"od", "pidof", "pkill", "readlink", "realpath", "rm", "rmdir", "sed", "setsid", "sh", "sha256sum", "sleep",
	"sort", "stat", "sync", "tail", "tar", "tee", "touch", "tr", "umount", "uname", "uniq", "wc", "xargs",
}

var (
	shellComment = regexp.MustCompile(`(?m)(^|\s)#.*$`)
	goString     = regexp.MustCompile("`[^`]*`|\"(?:[^\"\\\\\\n]|\\\\.)*\"")
)

// the code the device runs: the boot wrapper with pickle's hook, the inittab swap, and the string literals the Shell
// builds its commands from
func deviceCode(t *testing.T) string {
	t.Helper()
	var code strings.Builder
	for _, f := range []string{
		"../../../internal/install/files/cadence-run", "../files/board-hook", "../../../internal/install/swap-inittab.sh",
	} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		code.WriteString(shellComment.ReplaceAllString(string(raw), "$1"))
	}
	raw, err := os.ReadFile("../../../internal/install/shell.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range goString.FindAllString(string(raw), -1) {
		code.WriteString(s + "\n")
	}
	return code.String()
}

// a program the scripts run that preflight does not check would fail mid-swap, root writable or v1 half stopped
func TestToolsCoverTheScripts(t *testing.T) {
	code, tools := deviceCode(t), installer.Profile().Tools
	for _, a := range applets {
		if regexp.MustCompile(`(^|[^\w.-])`+regexp.QuoteMeta(a)+`($|[^\w.-])`).MatchString(code) && !slices.Contains(tools, a) {
			t.Errorf("the scripts run %s, which preflight does not check", a)
		}
	}
}
