package deviceops

import (
	"fmt"
	"strings"

	"git.shino.boo/devilreef/cadence/pkg/opsplan"
)

// Transport names.
const (
	TransportLink = "link"
	TransportAdb  = "adb"
	TransportSSH  = "ssh"
)

// A board's update profile builds a resolved plan from an artifact. Profiles are code, versioned
// with the server: the dashboard never accepts free-form steps (an owner's click runs one of
// these, exactly the runbooks' proven commands).
type profile struct {
	id string
	// supports says which transports may run it
	supports func(transport string) bool
	build    func(a *artifact, runID string) (*opsplan.Plan, error)
}

var profiles = map[string]profile{
	"yandex-hotswap": {id: "yandex-hotswap", supports: anyTransport(TransportLink, TransportAdb), build: yandexHotswap},
	"rpi-install":    {id: "rpi-install", supports: anyTransport(TransportLink, TransportSSH), build: rpiDeb},
	// a daemon cannot replace its own apk: adb alone installs it
	"android-tv-apk": {id: "android-tv-apk", supports: anyTransport(TransportAdb), build: tvAPK},
}

func anyTransport(names ...string) func(string) bool {
	return func(t string) bool {
		for _, n := range names {
			if t == n {
				return true
			}
		}
		return false
	}
}

// profileFor picks the profile an artifact's board and kind install with.
func profileFor(board, kind string) (profile, error) {
	// the vk capsula mini keeps the same tree under /data/cadence, and its cadence-run restarts what the swap kills
	yandex := board == "yandex-midi" || board == "yandex-micro" || board == "yandex-pickle" || board == "vk-capsula-mini"
	switch {
	case yandex && kind == "tar":
		return profiles["yandex-hotswap"], nil
	case board == "rpi" && kind == "deb":
		return profiles["rpi-install"], nil
	case board == "android-tv" && kind == "apk":
		return profiles["android-tv-apk"], nil
	case yandex:
		return profile{}, fmt.Errorf("%w: %s wants a tar artifact", ErrBadArtifact, board)
	case board == "rpi":
		return profile{}, fmt.Errorf("%w: rpi wants a deb artifact", ErrBadArtifact)
	case board == "android-tv":
		return profile{}, fmt.Errorf("%w: android-tv wants an apk artifact", ErrBadArtifact)
	}
	return profile{}, fmt.Errorf("%w: no profile for board %q", ErrBadArtifact, board)
}

// shq quotes a word for the shells our boards run (busybox sh, bash). Cadence's own paths and
// hashes carry none of these characters; anything that does is refused, not escaped.
func shq(s string) string {
	if strings.ContainsAny(s, "'\"\\\n`$(){};*?<>|&") {
		return ""
	}
	return s
}

func stage(name string, f *opsplan.FetchStep) opsplan.Step { return opsplan.Step{Name: name, Fetch: f} }
func run(name, cmd string) opsplan.Step {
	return opsplan.Step{Name: name, Exec: &opsplan.ExecStep{Cmd: cmd}}
}
func restart(name, cmd string) opsplan.Step {
	return opsplan.Step{Name: name, Exec: &opsplan.ExecStep{Cmd: cmd, Terminal: true}}
}

// yandexHotswap is the midi.md hot swap generalized: every bin/ file and the MANIFEST staged as
// .new, then swapped in one self-verifying step per file (the moved file's hash must equal the
// artifact's, else the run rolls back), and the wrapper's respawn takes the new tree. The whole
// bin/ set goes every time (idempotent, no on-device diff); a daemon fetching for itself skips a
// file already at its hash.
func yandexHotswap(a *artifact, runID string) (*opsplan.Plan, error) {
	const root = "/data/cadence"
	if shq(root) == "" {
		return nil, fmt.Errorf("%w: bad root", ErrBadArtifact)
	}
	p := &opsplan.Plan{ID: "yandex-hotswap", RunID: runID, Board: a.board, TargetVersion: a.version,
		Vars: map[string]string{"root": root, "version": a.version}, Restart: true}
	var files []opsplan.FileEntry
	for _, f := range a.files {
		if f.Path == "MANIFEST" || strings.HasPrefix(f.Path, "bin/") {
			files = append(files, f)
		}
	}
	if len(files) < 2 {
		return nil, fmt.Errorf("%w: no bin/ files in the artifact", ErrBadArtifact)
	}
	for _, f := range files {
		if shq(f.Path) == "" {
			return nil, fmt.Errorf("%w: bad path %q", ErrBadArtifact, f.Path)
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o755
		}
		p.Steps = append(p.Steps, stage("stage:"+f.Path, &opsplan.FetchStep{
			Src: "artifact:" + f.Path, Dst: root + "/" + f.Path + ".new", SHA256: f.SHA256, Mode: mode,
			Size: f.Size, SkipIfSame: true,
		}))
	}
	for _, f := range files {
		q := shq(f.Path)
		// cp -p keeps the old tree's file for the rollback; the moved file is checked against the
		// artifact's hash before the step counts as done
		p.Steps = append(p.Steps, run("swap:"+f.Path, fmt.Sprintf(
			"cd %[1]s && if [ -f %[2]s ]; then cp -p %[2]s %[2]s.prev; fi && mv %[2]s.new %[2]s && [ \"$(sha256sum %[2]s | cut -d' ' -f1)\" = %[3]s ]",
			shq(root), q, f.SHA256)))
	}
	p.Steps = append(p.Steps, restart("restart",
		"kill $(pidof cadence-audiod) $(pidof cadenced) 2>/dev/null; exit 0"))
	for _, f := range files {
		q := shq(f.Path)
		p.Rollback = append(p.Rollback, run("undo:"+f.Path, fmt.Sprintf(
			"cd %[1]s && if [ -f %[2]s.prev ]; then mv %[2]s.prev %[2]s; fi", shq(root), q)))
	}
	p.Rollback = append(p.Rollback, restart("restart",
		"kill $(pidof cadence-audiod) $(pidof cadenced) 2>/dev/null; exit 0"))
	return p, nil
}

// rpiDeb is the runbook's flow for the pi's deb: services stopped, apt installs the fetched deb
// (--reinstall so the same version goes again, --allow-downgrades so an owner's rollback to the
// previous artifact installs), the units reload and restart. The restart step is terminal: over
// ssh it returns, over the link the daemon dies with its unit.
func rpiDeb(a *artifact, runID string) (*opsplan.Plan, error) {
	deb := "/tmp/cadence-update-" + a.version + ".deb"
	p := &opsplan.Plan{ID: "rpi-install", RunID: runID, Board: a.board, TargetVersion: a.version,
		Vars: map[string]string{"version": a.version}, Restart: true}
	p.Steps = []opsplan.Step{
		run("stop", "systemctl stop cadence-screen.service cadence.service 2>/dev/null || true"),
		stage("stage:deb", &opsplan.FetchStep{Src: "artifact:", Dst: deb, SHA256: a.sha,
			Mode: 0o644, Size: int64(len(a.data))}),
		run("install", "env DEBIAN_FRONTEND=noninteractive apt-get update -qq && env DEBIAN_FRONTEND=noninteractive apt-get install -y --reinstall --allow-downgrades "+deb),
		run("reload", "systemctl daemon-reload && systemctl enable cadence.service cadence-screen.service"),
		run("cleanup", "rm -f "+deb),
		restart("restart", "systemctl restart cadence.service cadence-screen.service"),
	}
	// a failed install left the old tree's units stopped: start them back and let the owner retry
	p.Rollback = []opsplan.Step{
		restart("start", "systemctl start cadence.service cadence-screen.service"),
	}
	return p, nil
}

// tvAPK installs the apk over adb: push, pm install -r, then the launcher intent restarts the
// app (an update force-stops it; the activity brings DaemonService back). A downgrade adds -d,
// which is how an owner's rollback to an older build installs.
func tvAPK(a *artifact, runID string) (*opsplan.Plan, error) {
	const pkg = "dev.cadence.tv"
	flag := "-r"
	if a.downgrade {
		flag = "-r -d"
	}
	p := &opsplan.Plan{ID: "android-tv-apk", RunID: runID, Board: a.board, TargetVersion: a.version,
		Vars: map[string]string{"version": a.version}, Restart: true}
	p.Steps = []opsplan.Step{
		stage("stage:apk", &opsplan.FetchStep{Src: "artifact:", Dst: "/data/local/tmp/cadence-update.apk",
			SHA256: a.sha, Mode: 0o644, Size: int64(len(a.data))}),
		run("install", "pm install "+flag+" /data/local/tmp/cadence-update.apk"),
		run("cleanup", "rm -f /data/local/tmp/cadence-update.apk"),
		restart("start", "monkey -p "+pkg+" -c android.intent.category.LEANBOX_LAUNCHER 1 >/dev/null 2>&1 || am start -n "+pkg+"/.CadenceActivity"),
	}
	return p, nil
}
