// Command faketorrserver is a dev fake of a TorrServer MatriX.150
// (torrservertest's scripted library) over plain http. With
// CADENCE_FAKE_TORRSERVER_PASSWORD set, basic auth (user "cadence") guards
// what TorrServer guards. It logs methods, routes and statuses only.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrserver/torrservertest"
	"git.shino.boo/devilreef/cadence/server/tools/fakelog"
)

func main() {
	addr := flag.String("addr", ":8090", "the address to listen on")
	flag.Parse()
	lib := torrservertest.Scripted()
	if pw := os.Getenv("CADENCE_FAKE_TORRSERVER_PASSWORD"); pw != "" {
		lib.User, lib.Password = "cadence", pw
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := &http.Server{Addr: *addr, Handler: fakelog.Routes(log, torrservertest.New(lib)), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("listening", "addr", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve_failed")
		os.Exit(1)
	}
}
