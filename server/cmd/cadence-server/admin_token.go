package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// scopeFlags collects a repeated --scope.
type scopeFlags []string

func (s *scopeFlags) String() string { return strings.Join(*s, ",") }

func (s *scopeFlags) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// runTokenCreate creates an API token (M7a-D6) and prints it once on stdout;
// --scope repeats and defaults to admin, --mcp-tool repeats and narrows an
// mcp token to the tools it names. A bad name or scope is refused before the
// database is reached.
func runTokenCreate(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "")
	var scopes scopeFlags
	fs.Var(&scopes, "scope", "")
	var mcpTools scopeFlags
	fs.Var(&mcpTools, "mcp-tool", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *name == "" {
		_, _ = fmt.Fprintln(stderr, adminUsage)
		return 2
	}
	if !auth.ValidTokenName(*name) {
		_, _ = fmt.Fprintln(stderr, "token name: lowercase letters, digits, '.', '_' or '-', at most 64, starting with a letter or digit")
		return 2
	}
	if len(scopes) == 0 {
		scopes = scopeFlags{string(auth.ScopeAdmin)}
	}
	if _, err := auth.ParseScopes(scopes); err != nil {
		_, _ = fmt.Fprintln(stderr, "scope: admin, ha, metrics, presence or mcp")
		return 2
	}
	if _, err := auth.ParseMCPTools(mcpTools); err != nil {
		_, _ = fmt.Fprintln(stderr, "mcp tool: lowercase letters, digits, '.', '_' or '-', each once, at most 128")
		return 2
	}
	cfg, ok := loadConfig(getenv, stderr)
	if !ok {
		return 2
	}
	return openStore(cfg, stderr, func(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) int {
		tok, _, err := auth.NewTokens(db.New(pool), time.Now).Create(ctx, *name, scopes, mcpTools)
		switch {
		case errors.Is(err, auth.ErrTokenNameTaken):
			log.Error("token_create_failed", "code", "name_taken")
			return 1
		case err != nil:
			log.Error("token_create_failed", "code", "store")
			return 1
		}
		_, _ = fmt.Fprintln(stdout, tok)
		return 0
	})
}
