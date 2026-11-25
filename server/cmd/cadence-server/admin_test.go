package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/store"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

func buildBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cadence-server")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestAdminTokenCreatePrintsOnlyToken(t *testing.T) {
	bin := buildBin(t)
	dsn := storetest.URL(t)
	keyFile := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyFile, []byte("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "admin", "token", "create", "--name", "ci")
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dsn, "CADENCE_MASTER_KEY_FILE="+keyFile)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !regexp.MustCompile(`^cdn_[a-z2-7]{52}\n$`).MatchString(stdout.String()) {
		t.Fatalf("stdout shape: %d bytes", stdout.Len())
	}
	if strings.Contains(stderr.String(), strings.TrimSpace(stdout.String())) {
		t.Fatal("stderr leaks the token")
	}
}

func TestAdminUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"token"}, {"token", "create"}, {"token", "create", "--name"}, {"bogus"}, {"password"}, {"password", "reset"}, {"password", "set", "hunter2hunter2"},
		{"prompts"}, {"prompts", "render"}, {"prompts", "render", "--tts", "t", "--out", "o"},
		{"prompts", "render", "--tts", "t", "--lang", "RU", "--out", "o"}, {"prompts", "render", "--lang", "ru", "--out", "o"},
		{"prompts", "render", "--tts", "t", "--lang", "de", "--out", "o"}} {
		var out, errb bytes.Buffer
		if code := runAdmin(args, func(string) string { return "" }, strings.NewReader(""), &out, &errb); code != 2 {
			t.Fatalf("%v: code %d", args, code)
		}
		if out.Len() != 0 {
			t.Fatalf("%v: stdout not empty", args)
		}
	}
}

func keyFile(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(f, []byte("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// a short or missing password is refused before the database is reached
func TestAdminPasswordSetRefusesShort(t *testing.T) {
	env := envOf(map[string]string{"DATABASE_URL": "postgres://u:p@127.0.0.1:1/x?connect_timeout=1", "CADENCE_MASTER_KEY_FILE": keyFile(t)})
	for _, in := range []string{"", "\n", "short pw 11\n", "пароль-абвг\r\n"} {
		var out, errb bytes.Buffer
		if code := runAdmin([]string{"password", "set"}, env, strings.NewReader(in), &out, &errb); code != 2 {
			t.Fatalf("%q: code %d, stderr %s", in, code, errb.String())
		}
		if out.Len() != 0 || (strings.TrimSpace(in) != "" && strings.Contains(errb.String(), strings.TrimSpace(in))) {
			t.Fatalf("%q: stdout %q stderr %q", in, out.String(), errb.String())
		}
		if !strings.Contains(errb.String(), "at least 12 characters") {
			t.Fatalf("%q: stderr %q", in, errb.String())
		}
	}
}

// the password comes from stdin's first line (its newline dropped), sets the
// owner's hash and ends every session; nothing of it is printed
func TestAdminPasswordSet(t *testing.T) {
	ctx := context.Background()
	dsn := storetest.URL(t)
	pool, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// a password set before, with a session and a known browser open
	var at time.Time
	if err := pool.QueryRow(ctx, "INSERT INTO admin_users VALUES (1, 'x', now()) RETURNING updated_at").Scan(&at); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions(pool, time.Now)
	before, err := sessions.Open(ctx, at, "")
	if err != nil {
		t.Fatal(err)
	}
	env := envOf(map[string]string{"DATABASE_URL": dsn, "CADENCE_MASTER_KEY_FILE": keyFile(t)})
	const pw = "a long owner password"
	var out, errb bytes.Buffer
	if code := runAdmin([]string{"password", "set"}, env, strings.NewReader(pw+"\r\nignored second line\n"), &out, &errb); code != 0 {
		t.Fatalf("code %d, stderr %s", code, errb.String())
	}
	if out.Len() != 0 || strings.Contains(errb.String(), pw) || strings.Contains(errb.String(), dsn) {
		t.Fatalf("stdout %q stderr %q", out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), `"msg":"admin_password_set","sessions_revoked":1,"browsers_revoked":1`) {
		t.Fatalf("stderr %s", errb.String())
	}
	if _, err := sessions.Validate(ctx, before.Session.Token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("session survived: %v", err)
	}
	if known, err := sessions.KnownBrowser(ctx, before.Browser); known || err != nil {
		t.Fatalf("known browser survived: %v", err)
	}
	owner := auth.NewOwner(pool, time.Now)
	if ok, _, err := owner.Check(ctx, pw); err != nil || !ok {
		t.Fatalf("password does not check: %v %v", ok, err)
	}
	if ok, _, _ := owner.Check(ctx, pw+"\r"); ok {
		t.Fatal("the line ending became part of the password")
	}
}
