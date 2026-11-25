package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"git.shino.boo/devilreef/cadence/device/internal/enroll"
)

func runEnroll(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("cadenced enroll", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o enroll.Options
	fs.StringVar(&o.Server, "server", "", "server base url (required)")
	fs.StringVar(&o.Code, "code", "", "enrollment code (required)")
	fs.StringVar(&o.Board, "board", "", "board id (required)")
	fs.StringVar(&o.ConfigPath, "config", "", "config file to write (required)")
	fs.StringVar(&o.DataDir, "data-dir", "", "data directory (required)")
	fs.StringVar(&o.CAFile, "ca-file", "", "extra CA bundle for the server")
	fs.BoolVar(&o.Audiod, "audiod", false, "run the board's audio through cadence-audiod")
	fs.StringVar(&o.AudiodBinary, "audiod-binary", "", "absolute path of cadence-audiod (default: the built-in one)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 || o.Server == "" || o.Code == "" || o.Board == "" || o.ConfigPath == "" || o.DataDir == "" {
		_, _ = fmt.Fprintln(stderr, "cadenced enroll: --server, --code, --board, --config and --data-dir are required")
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	switch err := enroll.Run(ctx, o); {
	case err == nil:
		return 0
	case errors.Is(err, enroll.ErrRefused):
		_, _ = fmt.Fprintln(stderr, "cadenced enroll: code refused")
		return exitRefused
	case errors.Is(err, enroll.ErrInvalid):
		_, _ = fmt.Fprintln(stderr, "cadenced enroll:", err)
		return exitUsage
	default:
		_, _ = fmt.Fprintln(stderr, "cadenced enroll:", err)
		return 1
	}
}
