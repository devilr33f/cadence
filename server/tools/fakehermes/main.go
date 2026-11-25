// Command fakehermes is the e2e's Hermes agent server (deploy/dev/compose.yml),
// a dev fake that never ships in the server's image: the subset of the Runs
// API the server's hermes client speaks (the capabilities probe, idempotent
// runs, status polls, stops, clarify and approval answers) over https, each
// run scripted by the first word of its input (see run). Every request must
// carry the bearer key in CADENCE_FAKE_HERMES_KEY. It logs routes, run ids and
// statuses only.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", ":8443", "the address to listen on")
	cert := flag.String("cert", "", "the tls certificate")
	key := flag.String("key", "", "the certificate's key")
	release := flag.String("release", "", "the file whose existence ends hold runs")
	flag.Parse()
	apiKey := os.Getenv("CADENCE_FAKE_HERMES_KEY")
	if *cert == "" || *key == "" || apiKey == "" {
		fmt.Fprintln(os.Stderr, "fakehermes: -cert, -key and CADENCE_FAKE_HERMES_KEY are required")
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := &http.Server{
		Addr:              *addr,
		Handler:           newServer(config{Key: apiKey, Release: *release, Now: time.Now, Log: log}),
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
		os.Exit(1)
	}
}
