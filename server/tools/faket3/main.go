// Command faket3 is a fake T3 Code server for the e2e, a dev fake that never
// ships in the server's image: t3codetest's orchestration protocol 2 subset
// (Effect RPC JSON over /ws, the HTTP shell snapshot, websocket tickets)
// over https, its threads scripted by the first word of a message. Every
// request must carry the bearer token in CADENCE_FAKE_T3_TOKEN. With -calls
// it appends each command it applied to that file as `type thread_id` (no
// text). It logs methods, ids and codes only.
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
	"sync"
	"syscall"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code/t3codetest"
)

func main() {
	addr := flag.String("addr", ":8443", "the address to listen on")
	cert := flag.String("cert", "", "the tls certificate")
	key := flag.String("key", "", "the certificate's key")
	ticketOnly := flag.Bool("ticket-only", false, "refuse the bearer on the websocket upgrade, so clients buy a ticket")
	callsPath := flag.String("calls", "", "append each command applied to this file as `type thread_id`")
	flag.Parse()
	token := os.Getenv("CADENCE_FAKE_T3_TOKEN")
	if *cert == "" || *key == "" || token == "" {
		fmt.Fprintln(os.Stderr, "faket3: -cert, -key and CADENCE_FAKE_T3_TOKEN are required")
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	var calls io.Writer
	if *callsPath != "" {
		f, err := os.OpenFile(*callsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Error("calls_unwritable")
			os.Exit(1)
		}
		defer func() { _ = f.Close() }()
		calls = f
	}
	srv := &http.Server{
		Addr: *addr,
		Handler: t3codetest.New(t3codetest.Config{
			Token: token, TicketOnly: *ticketOnly, Step: 200 * time.Millisecond, Log: log, OnCommand: record(calls, log),
		}),
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

// record appends each command to w, one `type thread_id` line; nil without
// a file.
func record(w io.Writer, log *slog.Logger) func(t3codetest.Command) {
	if w == nil {
		return nil
	}
	var mu sync.Mutex
	return func(c t3codetest.Command) {
		mu.Lock()
		defer mu.Unlock()
		if _, err := fmt.Fprintf(w, "%s %s\n", c.Type, c.ThreadID); err != nil {
			log.Error("calls_failed")
		}
	}
}
