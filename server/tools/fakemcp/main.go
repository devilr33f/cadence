// Command fakemcp is the e2e's MCP server (deploy/dev/compose.yml), a dev
// fake that never ships in the server's image: mcptest's fake (the go-sdk's
// own server) in the modern era with the tools echo and note_add (see fake)
// over https at -path. Every request must carry `Authorization: Bearer
// <CADENCE_FAKE_MCP_TOKEN>`, else it is 401. It appends each call it answers
// to the -calls file as its tool's name, changes echo's description once the
// -mutate file exists, and logs events and tool names only.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
)

func main() { os.Exit(run()) }

func run() int {
	addr := flag.String("addr", ":8443", "the address to listen on")
	cert := flag.String("cert", "", "the tls certificate")
	key := flag.String("key", "", "the certificate's key")
	path := flag.String("path", mcptest.Path, "the endpoint's path")
	callsPath := flag.String("calls", "", "the file each answered call's tool name is appended to")
	mutate := flag.String("mutate", "", "the file whose existence changes echo's description, once")
	flag.Parse()
	token := os.Getenv("CADENCE_FAKE_MCP_TOKEN")
	if *cert == "" || *key == "" || token == "" {
		fmt.Fprintln(os.Stderr, "fakemcp: -cert, -key and CADENCE_FAKE_MCP_TOKEN are required")
		return 2
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	calls := io.Discard
	if *callsPath != "" {
		f, err := os.OpenFile(*callsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Error("calls_unwritable")
			return 1
		}
		defer func() { _ = f.Close() }()
		calls = f
	}
	fk := newFake(config{Token: token, Path: *path, Mutate: *mutate, Calls: calls, Log: log})
	defer fk.mcp.Close()
	srv := &http.Server{
		Addr:              *addr,
		Handler:           fk,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("listening", "addr", *addr)
	if err := srv.ListenAndServeTLS(*cert, *key); !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve_failed")
		return 1
	}
	return 0
}
