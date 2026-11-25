// Package install swaps a speaker that runs cadence v1 to v2 and back (cadence-install, M2-D1): it drives the device
// over ssh or adb with POSIX sh commands, keeps recovery access and a read-only root on every path (rules 2 and 3),
// keeps the vendor assistant off (rule 4: removed, or kept installed and suspended before every start of cadenced,
// M2m-R2), and never copies anything off the device. The board's facts come in a Profile, so no hardware is named
// here.
package install

import (
	"errors"
	"path"
)

// Profile is what the installer needs to know about a board. Its board package fills it.
type Profile struct {
	// Board is the board id: the tarball's "# board" header and enroll's --board
	Board string
	// Arch is the tarball's "# arch" header
	Arch string
	// DataRoot is where cadence lives; DataMount is the filesystem it is on, for the free space check
	DataRoot, DataMount string
	// Inittab is busybox init's table, on the root filesystem
	Inittab string
	// ModelPath holds the device's model, NULs stripped; it must start with ModelPrefix, and be Model when that is set
	ModelPath, ModelPrefix, Model string
	// KernelPath holds the kernel release, which must start with KernelFamily
	KernelPath, KernelFamily string
	// Facts are the firmware the board was tested on: a difference refuses unless the owner accepts it
	Facts []Fact
	// Variants are other firmwares the board was tested on: preflight picks the first whose facts all hold and has
	// it adjust the profile, before anything else is checked; none holding keeps the profile and its Facts
	Variants []Variant
	// Recovery are the inittab entries that keep the device reachable, and RecoveryDaemons the programs they run:
	// all must be there before and after every change (rule 2)
	Recovery        []string
	RecoveryDaemons []string
	// Anchor is the inittab entry cadence's own goes right after, as v1's install.sh placed it
	Anchor string
	// Vendor are the vendor assistant's and updater's files and VendorDaemons its programs: any of them present
	// refuses (rule 4), as does an inittab line naming one of InittabForbidden
	Vendor           []string
	VendorDaemons    []string
	InittabForbidden []string
	// Stock is the vendor assistant a board keeps installed (M2m-R2), nil on one whose provisioning deleted it
	Stock *Stock
	// PlatformDaemons are platform programs the board's hook stops while cadence runs (midi's bluetooth player and
	// pairing agent, which cadenced replaces): every restore path starts them again
	PlatformDaemons []Daemon
	// RequireOTABlocked is a board with no vendor OTA block of its own yet: every install and every restore of its
	// stock needs the owner's --ota-blocked, the word that the router blocks the vendor's OTA servers (M2m-R3)
	RequireOTABlocked bool
	// Hardening are the board's hardening steps; none is applied by Install yet (M2m-R3)
	Hardening []HardeningStep
	// MixerControl is the hardware volume control the wrapper and audiod set
	MixerControl string
	// Tools are the commands the installer runs on the device
	Tools []string
	// MinRootKB is the free space the inittab write needs on the root filesystem.
	MinRootKB int64
	// SpareKB is room beyond the staged tarball and tree; zero keeps the 64 MiB default.
	SpareKB int64
	// HexRead is a board whose busybox head has no -c and od no -A or -t: files are read with dd and hexdump
	HexRead bool
	// StageDir is where the tarball is pushed instead of the work dir, for a board whose data mount is too small to
	// hold it beside the tree; it is removed once unpacked or refused
	StageDir string
	// RefuseIfPresent refuses an install while any of its paths exists
	RefuseIfPresent []Refusal
	// OverlayRoot is a board whose / is an rw overlay over a read-only lower layer, which init reads its table from
	// before the overlay exists: nothing remounts /, and the inittab is never read or written
	OverlayRoot bool
	// InitScript is the init script, by its merged path, that boots cadence on an overlay root instead of an inittab
	// entry, and InitScriptLower its stock copy in the lower layer: install writes InitLauncher over it, and a restore
	// writes the lower copy's bytes back, never removing the merged path (that would leave a whiteout)
	InitScript, InitScriptLower string
	// InitLauncher is the launcher's bytes: it starts cadence-run detached, or the stock copy when cadence is missing
	InitLauncher []byte
	// InitPidFile is where the launcher has cadence-run's pid written: the one way to tell cadence-run from its
	// watchdog feeder, a subshell that carries the same command line
	InitPidFile string
	// BootPath is init's PATH, which the scripts the installer runs as init would (the launcher, the stock's and the
	// platform's init scripts, the wrapper's release) get instead of the session's, and which every other command
	// searches first, the session's PATH after it (a session's own busybox may lack sha256sum -c); empty keeps the
	// session's alone
	BootPath string
	// PlatformScripts are init scripts the board's hook stops while cadence runs: every restore path runs them with
	// start
	PlatformScripts []PlatformScript
}

// PlatformScript is a platform init script and the process it starts: a restore runs its start only while no process
// of that name runs, as the script itself does not check
type PlatformScript struct{ Script, Process string }

// bootErr is a profile that boots half by the inittab and half by an init script: an overlay root boots by its init
// script alone, with its stock copy and the launcher, and names no inittab
func (p Profile) bootErr() error {
	script := p.InitScript != ""
	switch {
	case p.OverlayRoot != script:
		return errors.New("an overlay root boots through an init script, and only an overlay root does")
	case script && (p.InitScriptLower == "" || len(p.InitLauncher) == 0 || p.InitPidFile == ""):
		return errors.New("an init script boot needs the script's lower copy, the launcher and its pid file")
	case script && (p.Inittab != "" || p.Anchor != "" || len(p.Recovery) > 0 || len(p.InittabForbidden) > 0):
		return errors.New("an overlay root's boot reads no inittab")
	}
	return nil
}

// Refusal is a path whose presence refuses an install, and why.
type Refusal struct{ Path, Reason string }

// Daemon is a platform program the restore paths start detached, with its arguments, unless one runs as Exe already.
type Daemon struct {
	Exe  string
	Args []string
}

// Variant is another tested firmware of a board: its Facts select it and replace the profile's, and Apply adjusts
// the rest of the profile for it (its recovery access, its stock's files, its hardening)
type Variant struct {
	Name  string
	Facts []Fact
	Apply func(*Profile)
}

// Fact is one firmware fact: the content of the first of Paths that exists, trimmed, must equal Want.
type Fact struct {
	Name  string
	Paths []string
	Want  string
}

// Stock is the vendor assistant a board keeps: the wrapper's board hook suspends everything run from Dir before every
// start of cadenced and while it runs, and --restore-stock starts it again. Only the presence of its files is checked,
// never their content (rule 5).
type Stock struct {
	// Dir is the vendor's directory: it must exist, and nothing may run from it while cadence runs
	Dir string
	// Start is the vendor's start script, which must be executable: --restore-stock runs it with sh, never the boot
	// routine its init script runs
	Start string
	// Launcher is the program Start leaves running, which supervises the rest: it must exist, and --restore-stock
	// waits for it
	Launcher string
	// Files must exist
	Files []string
}

// layout are cadence's own paths under a data root: v1's as its install.sh placed them, and v2's as the tarball and
// cadence-run lay them out
type layout struct {
	root string
	// the installer's
	state, work, backup, v1Dir, v2Dir string
	// v1's
	v1Entry, v1Init, v1Daemon, v1Media string
	v1Pidfiles                         []string
	// v2's; hook is the board's part of the wrapper, which sources it from beside itself
	v2Entry, wrapper, hook, daemon, audiod, config, ca, log, volume string
}

func layoutOf(root string) layout {
	at := func(p string) string { return path.Join(root, p) }
	return layout{
		root:  root,
		state: at("install.state"), work: at(".install"), backup: at("backup"), v1Dir: at("v1"), v2Dir: at("v2"),
		v1Entry: "::respawn:" + at("cadenced.init"), v1Init: at("cadenced.init"), v1Daemon: at("cadenced"),
		v1Media:    at("cadence-media"),
		v1Pidfiles: []string{at("run/cadenced.init.pid"), at("run/cadenced.pid")},
		v2Entry:    "::respawn:" + at("bin/cadence-run"), wrapper: at("bin/cadence-run"), hook: at("bin/board-hook"),
		daemon: at("bin/cadenced"), audiod: at("bin/cadence-audiod"), config: at("config.json"),
		ca: at("share/ca-certificates.crt"), log: at("log/cadenced.log"), volume: at("state/volume"),
	}
}

// kept are the data root's entries a swap never moves: the installer's own, the trees moved aside, and stock/, where
// v1 and v2 both find the owner's command model (pushed from outside the repo, rule 5)
var kept = map[string]bool{"install.state": true, ".install": true, "backup": true, "v1": true, "v2": true, "stock": true}
