// Command faketorrplayer is a dev fake of torrplayer's control API and media
// server (torrplayertest's scripted library) over plain http, as torrplayer
// serves its LAN API. Every /api/v1 request must carry the bearer token in
// CADENCE_FAKE_TORRPLAYER_TOKEN. It logs methods, routes and statuses only.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrplayer/torrplayertest"
	"git.shino.boo/devilreef/cadence/server/tools/fakelog"
)

func main() {
	addr := flag.String("addr", ":8092", "the address to listen on")
	flag.Parse()
	token := os.Getenv("CADENCE_FAKE_TORRPLAYER_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "faketorrplayer: CADENCE_FAKE_TORRPLAYER_TOKEN is required")
		os.Exit(2)
	}
	lib := torrplayertest.Frieren()
	lib.Token = token
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := &http.Server{Addr: *addr, Handler: fakelog.Routes(log, torrplayertest.New(lib)), ReadHeaderTimeout: 10 * time.Second}
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
