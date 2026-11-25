package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const passwordShort = "password: at least 12 characters"

// runPasswordSet sets the owner's dashboard password from stdin's first line
// and revokes every session and known browser (M7a-D4). The password is never an argument and
// never printed; a terminal does not echo it.
func runPasswordSet(getenv func(string) string, stdin io.Reader, stderr io.Writer) int {
	cfg, ok := loadConfig(getenv, stderr)
	if !ok {
		return 2
	}
	pw, err := readPassword(stdin, stderr)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "password: unreadable")
		return 2
	}
	if utf8.RuneCountInString(pw) < auth.MinPasswordLen {
		_, _ = fmt.Fprintln(stderr, passwordShort)
		return 2
	}
	return openStore(cfg, stderr, func(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) int {
		r, err := auth.NewOwner(pool, time.Now).SetPassword(ctx, pw)
		switch {
		case errors.Is(err, auth.ErrPasswordShort):
			_, _ = fmt.Fprintln(stderr, passwordShort)
			return 2
		case err != nil:
			log.Error("admin_password_failed")
			return 1
		}
		log.Info("admin_password_set", "sessions_revoked", r.Sessions, "browsers_revoked", r.Browsers)
		return 0
	})
}

// readPassword reads one line, its line ending dropped; from a terminal
// without echo.
func readPassword(stdin io.Reader, stderr io.Writer) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		_, _ = fmt.Fprint(stderr, "password: ")
		b, err := term.ReadPassword(int(f.Fd()))
		_, _ = fmt.Fprintln(stderr)
		return string(b), err
	}
	sc := bufio.NewScanner(stdin)
	if !sc.Scan() {
		return "", sc.Err()
	}
	return sc.Text(), nil
}
