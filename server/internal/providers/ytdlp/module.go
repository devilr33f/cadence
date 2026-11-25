package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Module is the yt-dlp player at bin (CADENCE_YTDLP; "" when the server has
// none, so no instance builds). Its one optional secret, cookies, is a
// Netscape cookies.txt of a signed-in YouTube session, as it is or in base64,
// for videos YouTube plays only to an account; the jar each run renews is
// saved back to it.
func Module(bin string, log *slog.Logger) providers.Module {
	return providers.Module{
		ID:              "yt-dlp",
		Kind:            providers.KindPlayer,
		ConfigSchema:    `{"type":"object","additionalProperties":false}`,
		OptionalSecrets: []string{"cookies"},
		Probe:           probe,
		NewSaving: func(_ json.RawMessage, sec map[string]string, save providers.SaveSecret) (any, error) {
			if bin == "" {
				return nil, errors.New("ytdlp: the server has no yt-dlp")
			}
			cookies, err := Cookies(sec["cookies"])
			if err != nil {
				return nil, err
			}
			return New(Config{Bin: bin, Cookies: cookies, Save: save, Log: log}), nil
		},
	}
}

// probe asks yt-dlp its version, which reaches nothing: the test says the
// program runs, not that YouTube answers it
func probe(ctx context.Context, v any) error {
	p, ok := v.(*Player)
	if !ok {
		return providers.ProbeBadResponse
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.cfg.Bin, "--ignore-config", "--version") //nolint:gosec // the path is bootstrap env
	cmd.Env = p.env()
	out, err := cmd.Output()
	switch {
	case ctx.Err() != nil:
		return providers.ProbeTimeout
	case err != nil || strings.TrimSpace(string(out)) == "":
		return providers.ProbeBadResponse
	}
	return nil
}
