// Command cadence-install swaps a Yandex speaker from cadence v1 to v2 (M2-D1) and back with --to-v1, over ssh with a
// key or adb; on a midi, which keeps its stock assistant, --restore-stock gives it back to the stock (M2m-D2). A VK
// Capsula Mini, which never ran v1, gets v2 through its stock's init script and goes back with --restore-stock. It runs
// on the owner's machine and is owner-gated: see deploy/pickle/README.md and deploy/midi/README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	vk "git.shino.boo/devilreef/cadence/device/boards/vk-capsula-mini/installer"
	micro "git.shino.boo/devilreef/cadence/device/boards/yandex-micro/installer"
	midi "git.shino.boo/devilreef/cadence/device/boards/yandex-midi/installer"
	pickle "git.shino.boo/devilreef/cadence/device/boards/yandex-pickle/installer"
	"git.shino.boo/devilreef/cadence/device/internal/install"
)

const (
	exitFailed  = 1
	exitUsage   = 2
	exitRefused = 3
)

// the transports' programs; tests point them nowhere
var sshBin, adbBin = "ssh", "adb"

// profiles are the boards cadence-install knows
var profiles = map[string]func() install.Profile{
	"yandex-pickle": pickle.Profile, "yandex-midi": midi.Profile, "yandex-micro": micro.Profile,
	"vk-capsula-mini": vk.Profile,
}

func main() {
	if file := os.Getenv(install.AskpassEnv); file != "" {
		os.Exit(askpass(file, os.Stdout, os.Stderr))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage: cadence-install discover [--wait 3s] [--json]
       cadence-install [--board yandex-pickle | yandex-midi | yandex-micro | vk-capsula-mini]
         (--ssh root@<ip> (--key <path> | --password-file <path>) [--known-hosts <path>] | --adb <ip:port>)...
         (--tarball <cadence-...-<board>-<arch>.tar> --server https://<host> [--code <code>] [--accept-firmware]
          [--ca-file <server's ca.crt>] | --to-v1 | --restore-stock) [--ota-blocked] [--dry-run]`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "discover" {
		return runDiscover(ctx, args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("cadence-install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	board := fs.String("board", "yandex-pickle", "the speaker's board")
	sshTarget := fs.String("ssh", "", "user@host of the speaker's dropbear")
	key := fs.String("key", "", "the ssh private key")
	passwordFile := fs.String("password-file", "", "a file holding the ssh password on its first line, for a dropbear without keys")
	knownHosts := fs.String("known-hosts", install.DefaultKnownHosts(), "known_hosts holding the speaker's host key")
	adbSerial := fs.String("adb", "", "ip:port of the speaker's adbd, the fallback")
	tarball := fs.String("tarball", "", "the tarball make device-build wrote")
	server := fs.String("server", "", "the server's https:// url, for enroll")
	code := fs.String("code", "", "the dashboard's enroll code, needed until the speaker is enrolled")
	accept := fs.Bool("accept-firmware", false, "accept firmware that differs from the tested one")
	caFile := fs.String("ca-file", "", "the server's own CA (PEM), for a server whose certificate no public CA signed")
	otaBlocked := fs.Bool("ota-blocked", false, "the router blocks the vendor's OTA servers (a midi needs it)")
	dryRun := fs.Bool("dry-run", false, "read the speaker and print each change instead of making it")
	toV1 := fs.Bool("to-v1", false, "swap back to v1")
	restoreStock := fs.Bool("restore-stock", false, "give a speaker that keeps its stock assistant back to it")
	bad := func(msg string) int {
		_, _ = fmt.Fprintf(stderr, "cadence-install: %s\n%s\n", msg, usage)
		return exitUsage
	}
	if err := fs.Parse(args); err != nil {
		return bad(err.Error())
	}
	back := *toV1 || *restoreStock
	switch {
	case fs.NArg() > 0:
		return bad("unexpected argument " + fs.Arg(0))
	case profiles[*board] == nil:
		return bad("unknown board " + *board + "; known: " + strings.Join(slices.Sorted(maps.Keys(profiles)), ", "))
	case *sshTarget == "" && *adbSerial == "":
		return bad("--ssh or --adb is required")
	case *sshTarget != "" && (*key == "") == (*passwordFile == ""):
		return bad("--ssh needs exactly one of --key or --password-file")
	case *sshTarget == "" && *passwordFile != "":
		return bad("--password-file is for --ssh")
	case *toV1 && *restoreStock:
		return bad("--to-v1 or --restore-stock, not both")
	case back && (*tarball != "" || *server != "" || *code != "" || *accept || *caFile != ""):
		return bad("--to-v1 and --restore-stock take no --tarball, --server, --code, --accept-firmware or --ca-file")
	case !back && *tarball == "":
		return bad("--tarball is required")
	}
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(stdout, "cadence-install: "+format+"\n", a...) }
	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "cadence-install: %v\n", err)
		if errors.Is(err, install.ErrRefused) {
			return exitRefused
		}
		return exitFailed
	}

	p := profiles[*board]()
	var extraCA []byte
	if *caFile != "" {
		raw, err := os.ReadFile(*caFile)
		if err != nil {
			return fail(fmt.Errorf("%w: --ca-file cannot be read", install.ErrRefused))
		}
		if extraCA, err = install.ExtraCA(raw); err != nil {
			return fail(fmt.Errorf("--ca-file: %w", err))
		}
	}
	var bundle *install.Bundle
	if !back {
		b, err := install.OpenBundle(*tarball, p.Board, p.Arch)
		if err != nil {
			return fail(err)
		}
		bundle = b
		say("tarball: cadence %s, %d bytes, sha256 %s", b.Version, b.Size, b.Sum)
	}

	var runners []install.Runner
	if *sshTarget != "" {
		pw := *passwordFile
		if pw != "" {
			abs, err := filepath.Abs(pw)
			if err != nil {
				return fail(errors.New("--password-file: no absolute path"))
			}
			pw = abs
		}
		runners = append(runners, &install.SSH{Target: *sshTarget, Key: *key, PasswordFile: pw, KnownHosts: *knownHosts, Bin: sshBin})
	}
	if *adbSerial != "" {
		a := &install.ADB{Serial: *adbSerial, Bin: adbBin}
		a.Connect(ctx)
		runners = append(runners, a)
	}
	answering, errs := install.Answering(ctx, runners)
	for _, err := range errs {
		_, _ = fmt.Fprintf(stderr, "cadence-install: %v\n", err)
	}
	if len(answering) == 0 {
		return fail(errors.New("no transport answers; an ssh host key must already be in " + *knownHosts))
	}
	var probes []func(context.Context) error
	for _, r := range answering {
		probes = append(probes, func(ctx context.Context) error {
			res, err := r.Run(ctx, "true")
			if err == nil && res.Code != 0 {
				err = fmt.Errorf("%s: true exited %d", r, res.Code)
			}
			return err
		})
	}
	say("using %s", answering[0])

	dev := install.NewShell(answering[0], install.ShellOptions{DataRoot: p.DataRoot, Inittab: p.Inittab, HexRead: p.HexRead, StageDir: p.StageDir, BootPath: p.BootPath, DryRun: *dryRun, Out: stdout})
	in := install.New(dev, p, install.Options{
		Bundle: bundle, Server: *server, Code: *code, AcceptFirmware: *accept, OTABlocked: *otaBlocked, DryRun: *dryRun,
		ExtraCA: extraCA, Recovery: probes, Out: stdout,
	})
	var err error
	switch {
	case *toV1:
		err = in.ToV1(ctx)
	case *restoreStock:
		err = in.RestoreStock(ctx)
	default:
		err = in.Install(ctx)
	}
	if err != nil {
		code := fail(err)
		if !back && !errors.Is(err, install.ErrRefused) {
			_, _ = fmt.Fprintln(stderr, "cadence-install: "+failHint(p))
		}
		return code
	}
	return 0
}

// failHint is the way on after an install that failed past its refusals: again, or back to what the board ran. a
// board that boots cadence by an init script never ran v1, and --ota-blocked is named only for a board that needs it
func failHint(p install.Profile) string {
	hint := "run it again to resume"
	if p.InitScript == "" {
		hint += ", or with --to-v1 to go back to v1"
	}
	if p.Stock != nil {
		restore := "--restore-stock"
		if p.RequireOTABlocked {
			restore += " --ota-blocked"
		}
		hint += ", or with " + restore + " to give it back to its stock"
	}
	return hint
}
