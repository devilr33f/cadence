// Command fakeha is the e2e's Home Assistant (deploy/dev/compose.yml), a dev
// fake that never ships in the server's image: hatest's WebSocket API fake
// with a scripted home (see house) over https. It takes only the token in
// CADENCE_FAKE_HA_TOKEN, appends each service call it answers to the -calls
// file as `domain service entity_id`, and logs domains and services only.
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
)

func main() { os.Exit(run()) }

func run() int {
	addr := flag.String("addr", ":8443", "the address to listen on")
	cert := flag.String("cert", "", "the tls certificate")
	key := flag.String("key", "", "the certificate's key")
	callsPath := flag.String("calls", "", "the file each answered service call is appended to")
	flag.Parse()
	token := os.Getenv("CADENCE_FAKE_HA_TOKEN")
	if *cert == "" || *key == "" || token == "" {
		fmt.Fprintln(os.Stderr, "fakeha: -cert, -key and CADENCE_FAKE_HA_TOKEN are required")
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
	h := newHouse(token, calls, log, time.Now)
	defer h.fake.Close()
	srv := &http.Server{
		Addr:              *addr,
		Handler:           h.fake,
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
