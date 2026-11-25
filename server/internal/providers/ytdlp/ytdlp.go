// Package ytdlp resolves a YouTube video to an audio stream the device plays,
// by running yt-dlp (its path is bootstrap, CADENCE_YTDLP). Its stream URLs,
// its output and the cookies never reach an error or log: lines carry codes,
// exit statuses and timings only.
package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytmusic"
)

const (
	defaultTimeout = 25 * time.Second
	// MaxURLBytes is the longest url the device's media.url takes
	MaxURLBytes = 2047
	maxOutput   = 64 << 10
	// audio the device's ffmpeg decodes: aac in mp4 (itag 140) first
	format = "bestaudio[ext=m4a]/bestaudio[acodec^=mp4a]"
	// at most this many runs at once: each is a python process
	maxRuns = 2
)

// env passed through to yt-dlp: a proxy the server uses for YouTube
var passEnv = []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "no_proxy", "all_proxy"}

type Config struct {
	// Bin is yt-dlp's path
	Bin string
	// Cookies is a Netscape cookies.txt (Cookies' answer), or nil
	Cookies []byte
	// Save stores the jar yt-dlp renewed (YouTube rotates its session
	// cookies, and a jar left behind stops signing in); nil keeps it in memory
	Save providers.SaveSecret
	// Timeout bounds one run; zero means 25 s
	Timeout time.Duration
	// TempDir holds the cookies file of a run and the caches; "" is os.TempDir
	TempDir string
	Log     *slog.Logger
	// Now is the clock a format's available_at is read against; nil is time.Now
	Now func() time.Time
}

// Player runs yt-dlp; it is a providers.Player.
type Player struct {
	cfg  Config
	runs chan struct{}
	// cookieLock is held across a run with cookies: each run starts from the jar the one before it left, never two
	// from the same jar, whose second write-back would undo the first one's rotation
	cookieLock chan struct{}
	// jar is the latest cookies.txt, renewed after each run; only a holder of cookieLock touches it
	jar []byte
}

func New(c Config) *Player {
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.TempDir == "" {
		c.TempDir = os.TempDir()
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Player{cfg: c, runs: make(chan struct{}, maxRuns), cookieLock: make(chan struct{}, 1), jar: c.Cookies}
}

func (p *Player) Resolve(ctx context.Context, videoID string) (providers.Stream, error) {
	if !ytmusic.ValidVideoID(videoID) {
		return providers.Stream{}, providers.ErrUnplayable
	}
	ctx, finish := p.beginProgress(ctx, "audio")
	var resolveErr error
	defer func() { finish(resolveErr) }()
	// available_at: youtube answers 403 to a format's url fetched before it, and the relay fetches at once
	args := []string{"-f", format, "--print", "%(duration)s", "--print", "%(available_at)s", "--print", "urls"}
	out, err := p.do(ctx, "audio", args, "https://music.youtube.com/watch?v="+videoID, maxOutput, true)
	if err != nil {
		resolveErr = err
		return providers.Stream{}, err
	}
	s, availableAt, ok := parseOutput(out)
	if !ok {
		resolveErr = errors.New(providers.CodePlayerFailed)
		p.cfg.Log.Warn("ytdlp_failed", "mode", "audio", "code", "bad_output")
		return providers.Stream{}, resolveErr
	}
	progressReporter(ctx)(ResolveEvent{Stage: playprogress.StagePick, RemainingMS: -1, ACodec: "aac", AudioTracks: 1})
	if err := p.waitAvailable(ctx, availableAt); err != nil {
		resolveErr = err
		return providers.Stream{}, err
	}
	return s, nil
}

// do runs yt-dlp once with args and target after --, under the run limit
// (and the cookies lock when withCookies and a jar is set), and answers its
// stdout; failures are logged by code, mode, exit and time alone
func (p *Player) do(ctx context.Context, mode string, args []string, target string, maxOut int, withCookies bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	if f := progressReporter(ctx); f != nil && withCookies {
		f(ResolveEvent{Stage: playprogress.StageCookies, RemainingMS: -1})
	}
	cookies := withCookies && p.jar != nil
	if cookies {
		select {
		case p.cookieLock <- struct{}{}:
			defer func() { <-p.cookieLock }()
		case <-ctx.Done():
			p.cfg.Log.Warn("ytdlp_failed", "mode", mode, "code", "busy")
			return "", errors.New(providers.CodePlayerFailed)
		}
	}
	select {
	case p.runs <- struct{}{}:
		defer func() { <-p.runs }()
	case <-ctx.Done():
		p.cfg.Log.Warn("ytdlp_failed", "mode", mode, "code", "busy")
		return "", errors.New(providers.CodePlayerFailed)
	}
	start := time.Now()
	out, code, exit, err := p.run(ctx, args, target, maxOut, cookies)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		p.cfg.Log.Warn("ytdlp_failed", "mode", mode, "code", code, "exit", exit, "ms", ms, "cookies", cookies)
		return "", err
	}
	p.cfg.Log.Info("ytdlp_resolved", "mode", mode, "ms", ms, "cookies", cookies)
	return out, nil
}

func (p *Player) run(ctx context.Context, extra []string, target string, maxOut int, cookies bool) (string, string, int, error) {
	failed := errors.New(providers.CodePlayerFailed)
	args := []string{
		"--ignore-config", "--no-playlist", "--no-warnings", "--no-progress", "--force-ipv4",
		"--socket-timeout", "10", "--cache-dir", filepath.Join(p.cfg.TempDir, "cadence-yt-dlp"),
	}
	args = append(args, extra...)
	if cookies {
		// yt-dlp writes the jar back at exit, the cookies youtube rotated in it: read again after the run
		f, err := os.CreateTemp(p.cfg.TempDir, "cadence-cookies-*")
		if err != nil {
			return "", "cookies_file", -1, failed
		}
		defer func() { _ = os.Remove(f.Name()) }()
		_, werr := f.Write(p.jar)
		if cerr := f.Close(); werr != nil || cerr != nil {
			return "", "cookies_file", -1, failed
		}
		defer p.renew(f.Name())
		args = append(args, "--cookies", f.Name())
	}
	args = append(args, "--", target)
	// the path is bootstrap env; the one argument from outside is a checked video id or a search, after --
	cmd := exec.CommandContext(ctx, p.cfg.Bin, args...) //nolint:gosec // see above
	cmd.Env = p.env()
	cmd.WaitDelay = 2 * time.Second
	if f := progressReporter(ctx); f != nil {
		f(ResolveEvent{Stage: playprogress.StageExtract, RemainingMS: -1})
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limited{w: &stdout, n: maxOut}
	cmd.Stderr = newStageStderr(&stderr, maxOutput, func(stage playprogress.Stage) {
		if f := progressReporter(ctx); f != nil {
			f(ResolveEvent{Stage: stage, RemainingMS: -1})
		}
	})
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", "timeout", -1, failed
	}
	if err != nil {
		exit := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		}
		code, cerr := classify(stderr.String())
		return "", code, exit, cerr
	}
	if stdout.Len() >= maxOut {
		return "", "too_long", 0, failed
	}
	if f := progressReporter(ctx); f != nil {
		f(ResolveEvent{Stage: playprogress.StageFormats, RemainingMS: -1})
	}
	return stdout.String(), "", 0, nil
}

func (p *Player) env() []string {
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + p.cfg.TempDir,
		"XDG_CACHE_HOME=" + filepath.Join(p.cfg.TempDir, "cadence-cache"),
		"DENO_DIR=" + filepath.Join(p.cfg.TempDir, "cadence-deno"),
	}
	for _, k := range passEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// parseOutput reads the duration line, then the one stream url
// parseOutput reads duration, available_at (NA when youtube sets none) and the url; a run without the
// available_at line (two lines) is read as one with none
func parseOutput(out string) (providers.Stream, float64, bool) {
	lines := strings.Fields(out)
	if len(lines) != 2 && len(lines) != 3 {
		return providers.Stream{}, 0, false
	}
	u := lines[len(lines)-1]
	if !strings.HasPrefix(u, "https://") || len(u) > MaxURLBytes {
		return providers.Stream{}, 0, false
	}
	s := providers.Stream{URL: u}
	if f, err := strconv.ParseFloat(lines[0], 64); err == nil && f > 0 {
		s.DurationS = int(f + 0.5)
	}
	var availableAt float64
	if len(lines) == 3 {
		if f, err := strconv.ParseFloat(lines[1], 64); err == nil && f > 0 {
			availableAt = f
		}
	}
	return s, availableAt, true
}

// classify names a failed run by yt-dlp's error text, which is never kept
func classify(stderr string) (string, error) {
	e := strings.ToLower(stderr)
	for _, s := range []string{"sign in to", "login required", "members-only", "join this channel", "private video", "use --cookies"} {
		if strings.Contains(e, s) {
			return "sign_in_required", providers.ErrSignInRequired
		}
	}
	for _, s := range []string{"video unavailable", "not available", "has been removed", "no longer available", "requested format"} {
		if strings.Contains(e, s) {
			return "unplayable", providers.ErrUnplayable
		}
	}
	return "failed", errors.New(providers.CodePlayerFailed)
}

// limited keeps the first n bytes and drops the rest, so a chatty run
// neither blocks nor grows the server
type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(b []byte) (int, error) {
	if l.n > 0 {
		k := min(len(b), l.n)
		_, _ = l.w.Write(b[:k])
		l.n -= k
	}
	return len(b), nil
}

func (p *Player) now() time.Time { return p.cfg.Now() }
