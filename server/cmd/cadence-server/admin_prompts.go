package main

import (
	"archive/tar"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/app"
	"git.shino.boo/devilreef/cadence/server/internal/config"
	"git.shino.boo/devilreef/cadence/server/internal/prompts"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

var (
	promptLangRe = regexp.MustCompile(`^[a-z]{2,3}$`)
	codeRe       = regexp.MustCompile(`^[a-z_]{1,32}$`)
)

// runPromptsRender speaks the prompt table's phrases in one language through a TTS provider instance the server
// already has, and writes <out>/<lang>/<id>.opus for the device tarballs' share/prompts, or with --out - a tar of
// <lang>/<id>.opus on stdout (the server's container has no shell to copy files out with). The provider's key never
// leaves the database; only file names, sizes and codes are printed.
func runPromptsRender(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prompts render", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	tts := fs.String("tts", "", "")
	lang := fs.String("lang", "", "")
	voice := fs.String("voice", "", "")
	out := fs.String("out", "", "")
	tablePath := fs.String("table", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *tts == "" || *out == "" || !promptLangRe.MatchString(*lang) {
		_, _ = fmt.Fprintln(stderr, adminUsage)
		return 2
	}
	table, err := readTable(*tablePath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "table: "+err.Error())
		return 2
	}
	todo := prompts.For(table, *lang)
	if len(todo) == 0 {
		_, _ = fmt.Fprintln(stderr, "table: no prompt in "+*lang)
		return 2
	}
	cfg, ok := loadConfig(getenv, stderr)
	if !ok {
		return 2
	}
	write, finish, err := promptSink(*out, stdout)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "out: cannot create the directory")
		return 1
	}
	return openStore(cfg, stderr, func(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) int {
		engine, v, code := promptTTS(ctx, cfg, pool, log, *tts, *voice)
		if code != 0 {
			return code
		}
		failed := 0
		for _, p := range todo {
			file, err := prompts.Render(ctx, engine, v, p, prompts.DefaultEncoder)
			if err != nil {
				failed++
				log.Error("prompt_render_failed", "id", p.ID, "lang", p.Lang, "code", renderCode(err))
				continue
			}
			if err := write(p.Lang+"/"+p.ID+".opus", file); err != nil {
				failed++
				log.Error("prompt_render_failed", "id", p.ID, "lang", p.Lang, "code", "write")
				continue
			}
			log.Info("prompt_rendered", "id", p.ID, "lang", p.Lang, "bytes", len(file))
		}
		if err := finish(); err != nil {
			log.Error("prompt_render_failed", "code", "write")
			return 1
		}
		if failed > 0 {
			return 1
		}
		return 0
	})
}

// promptSink writes each file under dir, or into a tar on stdout for "-"
func promptSink(dir string, stdout io.Writer) (write func(name string, b []byte) error, finish func() error, err error) {
	if dir == "-" {
		tw := tar.NewWriter(stdout)
		madeDirs := map[string]bool{}
		return func(name string, b []byte) error {
			if d := path.Dir(name); !madeDirs[d] {
				madeDirs[d] = true
				if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d + "/", Mode: 0o755, ModTime: time.Now()}); err != nil {
					return err
				}
			}
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(b)), ModTime: time.Now()}); err != nil {
				return err
			}
			_, err := tw.Write(b)
			return err
		}, tw.Close, nil
	}
	return func(name string, b []byte) error {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // shipped to every device, no secret
			return err
		}
		return os.WriteFile(p, b, 0o644) //nolint:gosec // shipped to every device, no secret
	}, func() error { return nil }, nil
}

// promptTTS is the named TTS instance and the voice to speak in: the one given, else the global voice setting
func promptTTS(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger, name, voice string) (providers.TTS, string, int) {
	box, err := secrets.Load(cfg.MasterKeyFile)
	if err != nil {
		log.Error("prompt_render_failed", "code", "master_key")
		return nil, "", 1
	}
	engine, err := providers.NewInstances(pool, box, app.Registry(cfg, log)).TTS(ctx, name)
	if err != nil {
		code := "build"
		switch {
		case errors.Is(err, providers.ErrNotFound):
			code = "not_found"
		case errors.Is(err, providers.ErrWrongKind):
			code = "not_tts"
		}
		log.Error("prompt_render_failed", "code", code)
		return nil, "", 1
	}
	if voice == "" {
		global, err := settings.NewService(pool).Global(ctx)
		if err != nil {
			log.Error("prompt_render_failed", "code", "settings")
			return nil, "", 1
		}
		v, err := settings.Merge(global, nil)
		if err != nil {
			log.Error("prompt_render_failed", "code", "settings")
			return nil, "", 1
		}
		voice = v.Voice
	}
	return engine, voice, 0
}

func readTable(path string) ([]prompts.Prompt, error) {
	if path == "" {
		return prompts.Table()
	}
	f, err := os.Open(path) //nolint:gosec // the owner's own table
	if err != nil {
		return nil, errors.New("unreadable")
	}
	defer func() { _ = f.Close() }()
	return prompts.Parse(f)
}

// renderCode is a fixed code for a render's failure: a provider's own code when it has one
func renderCode(err error) string {
	switch {
	case errors.Is(err, prompts.ErrTooLong):
		return "too_long"
	case errors.Is(err, prompts.ErrNoAudio):
		return "no_audio"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	// a tts error is a fixed code (providers.TTSStream); anything else is not printed
	if msg := err.Error(); codeRe.MatchString(msg) {
		return msg
	}
	return providers.CodeTTSFailed
}
