package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/config"
	"git.shino.boo/devilreef/cadence/server/internal/store"
)

const adminUsage = "usage: cadence-server admin token create --name <name> [--scope admin|ha|metrics|presence]... | admin password set (the password on stdin) | admin openapi [-o path] | admin prompts render --tts <instance> --lang <ru|en> --out <dir>|- [--voice <voice>] [--table <tsv>]"

// runAdmin prints only a created token, or the prompts tar of --out -, on stdout; everything else goes to stderr.
func runAdmin(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	switch {
	case len(args) > 0 && args[0] == "openapi":
		return runOpenAPI(args[1:], stdout, stderr)
	case len(args) == 2 && args[0] == "password" && args[1] == "set":
		return runPasswordSet(getenv, stdin, stderr)
	case len(args) >= 2 && args[0] == "prompts" && args[1] == "render":
		return runPromptsRender(args[2:], getenv, stdout, stderr)
	case len(args) >= 2 && args[0] == "token" && args[1] == "create":
		return runTokenCreate(args[2:], getenv, stdout, stderr)
	}
	_, _ = fmt.Fprintln(stderr, adminUsage)
	return 2
}

// loadConfig reads the environment; an error names its variable on stderr.
func loadConfig(getenv func(string) string, stderr io.Writer) (config.Config, bool) {
	cfg, err := config.Load(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "config: "+err.Error())
		return config.Config{}, false
	}
	return cfg, true
}

// openStore runs fn on a migrated pool, logging json to stderr.
func openStore(cfg config.Config, stderr io.Writer, fn func(context.Context, *pgxpool.Pool, *slog.Logger) int) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	slog.SetDefault(log)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// driver errors can carry the url, so only the stage is logged
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("db_unavailable", "stage", "connect")
		return 1
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		log.Error("db_unavailable", "stage", "migrate")
		return 1
	}
	return fn(ctx, pool, log)
}
