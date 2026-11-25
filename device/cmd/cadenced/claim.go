package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/claim"
	"git.shino.boo/devilreef/cadence/device/internal/enroll"
	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
)

// the boards that join device-first, and where each one's screen connects; empty is <data-dir>/run/screen.sock
var claimBoards = map[string]string{"sim-tv": "", "rpi": "", "android-tv": "@cadence-screen"}

// runClaim enrols a screen board device-first: it serves the screen plane alone, shows the claim's code and qr there
// and writes the config once the owner approves it (or a dashboard code is typed on the screen). It exits 0 then; the
// board's wrapper starts cadenced on the config as after enroll.
func runClaim(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("cadenced claim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o claim.Options
	fs.StringVar(&o.Server, "server", "", "server base url (required)")
	fs.StringVar(&o.Board, "board", "", "screen board id (required): sim-tv, rpi or android-tv")
	fs.StringVar(&o.ConfigPath, "config", "", "config file to write (required)")
	fs.StringVar(&o.DataDir, "data-dir", "", "data directory (required)")
	fs.StringVar(&o.CAFile, "ca-file", "", "extra CA bundle for the server")
	fs.BoolVar(&o.Audiod, "audiod", false, "run the board's audio through cadence-audiod")
	fs.StringVar(&o.AudiodBinary, "audiod-binary", "", "absolute path of cadence-audiod (default: the built-in one)")
	socket := fs.String("screen-socket", "", "the screen plane's socket (default: the board's)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 || o.Server == "" || o.Board == "" || o.ConfigPath == "" || o.DataDir == "" {
		_, _ = fmt.Fprintln(stderr, "cadenced claim: --server, --board, --config and --data-dir are required")
		return exitUsage
	}
	def, ok := claimBoards[o.Board]
	if !ok {
		_, _ = fmt.Fprintln(stderr, "cadenced claim: not a screen board")
		return exitUsage
	}
	if *socket == "" {
		*socket = def
	}
	if *socket == "" {
		*socket = filepath.Join(o.DataDir, "run", "screen.sock")
	}
	if (*socket)[0] != '@' {
		if err := os.MkdirAll(filepath.Dir(*socket), 0o700); err != nil {
			_, _ = fmt.Fprintln(stderr, "cadenced claim: cannot make the socket's directory")
			return 1
		}
	}
	lg := newLogging(stderr)
	o.Log = lg.log
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	codes := make(chan string, 1)
	o.Codes = codes
	servers := make(chan string, 1)
	o.Servers = servers
	srv := screen.New(screen.Options{
		Path: *socket, DaemonVersion: version, Log: lg.log,
		EnrollCode: func(c string) {
			select {
			case codes <- c:
			default:
			}
		},
		// the latest address wins: one still waiting is replaced
		ServerAddress: func(u string) { latest(servers, u) },
	})
	o.Show = srv.ShowClaim
	srv.SetServerURL(o.Server)
	o.ServerChanged = srv.SetServerURL
	var wg sync.WaitGroup
	sctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		wg.Wait()
	}()
	wg.Go(func() { _ = srv.Run(sctx) })
	if o.Board == "sim-tv" && os.Getenv("CADENCE_SIM_TV_FAKE_SCREEN") == "1" {
		wg.Go(func() { fakeScreen(sctx, *socket) })
	}
	switch err := claim.Run(ctx, o); {
	case err == nil:
		// the screen sees approved before the plane closes
		time.Sleep(200 * time.Millisecond)
		return 0
	case errors.Is(err, context.Canceled):
		return 0
	case errors.Is(err, enroll.ErrInvalid):
		_, _ = fmt.Fprintln(stderr, "cadenced claim:", err)
		return exitUsage
	default:
		_, _ = fmt.Fprintln(stderr, "cadenced claim:", err)
		return 1
	}
}

func latest(ch chan string, v string) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}

// fakeScreen is the sim's screen while it claims, as simtv's is once enrolled
func fakeScreen(ctx context.Context, path string) {
	s, err := screentest.Connect(ctx, path, screentest.Options{})
	if err != nil {
		return
	}
	<-ctx.Done()
	s.Close()
}
