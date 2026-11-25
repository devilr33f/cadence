package ytdlp_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
)

const (
	streamURL = "https://rr1---sn-x.googlevideo.com/videoplayback?expire=1&sig=SIGNED-SECRET"
	cookieTxt = ".youtube.com\tTRUE\t/\tTRUE\t1893456000\tSID\tCOOKIE-SECRET\n" +
		"#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t1893456000\tHSID\tOTHER\n"
)

// fakeBin writes a yt-dlp stand-in: it records its arguments, and the
// cookies file it was given, then runs body
func fakeBin(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake yt-dlp is a shell script")
	}
	dir = t.TempDir()
	bin = filepath.Join(dir, "yt-dlp")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + dir + "/args\n" +
		"prev=\nfor a in \"$@\"; do if [ \"$prev\" = --cookies ]; then cat \"$a\" > " + dir + "/cookies; fi; prev=$a; done\n" +
		body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func logger(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewTextHandler(buf, nil)) }

func TestResolveAnswersTheStreamAndLogsNoURL(t *testing.T) {
	bin, dir := fakeBin(t, "echo 187.6; echo '"+streamURL+"'")
	var log bytes.Buffer
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&log)})
	s, err := p.Resolve(context.Background(), "5qZQEq_C3vc")
	if err != nil {
		t.Fatal(err)
	}
	if s.URL != streamURL || s.DurationS != 188 {
		t.Fatalf("stream %+v", s)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	got := string(args)
	for _, want := range []string{"--ignore-config\n", "--no-playlist\n", "--force-ipv4\n", "bestaudio[ext=m4a]", "--\nhttps://music.youtube.com/watch?v=5qZQEq_C3vc\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--cookies") {
		t.Error("cookies passed without a secret")
	}
	if strings.Contains(log.String(), "googlevideo") || !strings.Contains(log.String(), "ytdlp_resolved") {
		t.Fatalf("log: %s", log.String())
	}
}

func TestResolveGivesCookiesInAFileItRemoves(t *testing.T) {
	bin, dir := fakeBin(t, "echo 10; echo '"+streamURL+"'")
	cookies, err := ytdlp.Cookies(cookieTxt)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	var log bytes.Buffer
	p := ytdlp.New(ytdlp.Config{Bin: bin, Cookies: cookies, TempDir: tmp, Log: logger(&log)})
	if _, err := p.Resolve(context.Background(), "5qZQEq_C3vc"); err != nil {
		t.Fatal(err)
	}
	given, _ := os.ReadFile(filepath.Join(dir, "cookies"))
	if !bytes.Equal(given, cookies) {
		t.Fatalf("cookies file %q", given)
	}
	left, _ := filepath.Glob(filepath.Join(tmp, "cadence-cookies-*"))
	if len(left) != 0 {
		t.Fatalf("cookies file left: %v", left)
	}
	if strings.Contains(log.String(), "COOKIE-SECRET") {
		t.Fatal("log carries the cookies")
	}
}

func TestResolveNamesFailuresByCodeAlone(t *testing.T) {
	cases := []struct {
		name, body string
		want       error
	}{
		{"age", "echo 'ERROR: [youtube] x: Sign in to confirm your age. LEAK' >&2; exit 1", providers.ErrSignInRequired},
		{"bot", "echo \"ERROR: Sign in to confirm you're not a bot. Use --cookies LEAK\" >&2; exit 1", providers.ErrSignInRequired},
		{"gone", "echo 'ERROR: [youtube] x: Video unavailable LEAK' >&2; exit 1", providers.ErrUnplayable},
		{"format", "echo 'ERROR: Requested format is not available LEAK' >&2; exit 1", providers.ErrUnplayable},
		{"other", "echo 'ERROR: HTTP Error 500 LEAK' >&2; exit 1", errors.New(providers.CodePlayerFailed)},
		{"no url", "echo 10; echo http://plain", errors.New(providers.CodePlayerFailed)},
		{"too long", "echo 10; echo https://x/" + strings.Repeat("a", ytdlp.MaxURLBytes), errors.New(providers.CodePlayerFailed)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, _ := fakeBin(t, c.body)
			var log bytes.Buffer
			p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&log)})
			_, err := p.Resolve(context.Background(), "5qZQEq_C3vc")
			if err == nil || err.Error() != c.want.Error() {
				t.Fatalf("err %v, want %v", err, c.want)
			}
			if strings.Contains(log.String(), "LEAK") || strings.Contains(err.Error(), "LEAK") {
				t.Fatal("upstream text surfaced")
			}
		})
	}
}

func TestResolveTimesOut(t *testing.T) {
	bin, _ := fakeBin(t, "exec sleep 5")
	var log bytes.Buffer
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&log), Timeout: 200 * time.Millisecond})
	start := time.Now()
	_, err := p.Resolve(context.Background(), "5qZQEq_C3vc")
	if err == nil || time.Since(start) > 3*time.Second || !strings.Contains(log.String(), "code=timeout") {
		t.Fatalf("err %v after %v, log %s", err, time.Since(start), log.String())
	}
}

func TestResolveRefusesAnythingButAVideoID(t *testing.T) {
	bin, dir := fakeBin(t, "echo 10; echo '"+streamURL+"'")
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir()})
	for _, id := range []string{"", "--exec=rm", "https://evil.example/x", "5qZQEq_C3v", "5qZQEq_C3vc0"} {
		if _, err := p.Resolve(context.Background(), id); !errors.Is(err, providers.ErrUnplayable) {
			t.Fatalf("%q: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "args")); err == nil {
		t.Fatal("yt-dlp ran")
	}
}

func TestCookiesTakesRawOrBase64(t *testing.T) {
	raw, err := ytdlp.Cookies(cookieTxt)
	if err != nil || !bytes.HasPrefix(raw, []byte("# Netscape HTTP Cookie File\n")) {
		t.Fatalf("%v %q", err, raw)
	}
	enc := base64.StdEncoding.EncodeToString([]byte("# Netscape HTTP Cookie File\r\n" + cookieTxt))
	// a field may wrap the base64
	b64, err := ytdlp.Cookies(enc[:20] + "\n " + enc[20:])
	if err != nil || !bytes.Equal(b64, raw) {
		t.Fatalf("%v %q", err, b64)
	}
	if c, err := ytdlp.Cookies("  "); c != nil || err != nil {
		t.Fatalf("empty: %q %v", c, err)
	}
	for _, bad := range []string{"not cookies", "a\tb\tc", "# only a comment\t\n", base64.StdEncoding.EncodeToString([]byte("x\ty"))} {
		if _, err := ytdlp.Cookies(bad); !errors.Is(err, ytdlp.ErrBadCookies) || strings.Contains(err.Error(), bad) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestModule(t *testing.T) {
	if _, err := ytdlp.Module("", nil).NewSaving(nil, nil, nil); err == nil {
		t.Fatal("built with no yt-dlp")
	}
	bin, _ := fakeBin(t, "echo 2026.10.26")
	m := ytdlp.Module(bin, nil)
	if _, err := m.NewSaving(nil, map[string]string{"cookies": "garbage"}, nil); err == nil {
		t.Fatal("built with bad cookies")
	}
	p, err := m.NewSaving(nil, map[string]string{"cookies": cookieTxt}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Probe(context.Background(), p); err != nil {
		t.Fatalf("probe %v", err)
	}
	broken, _ := fakeBin(t, "exit 3")
	pb, _ := ytdlp.Module(broken, nil).NewSaving(nil, nil, nil)
	if got := providers.ProbeCode(m.Probe(context.Background(), pb)); got != "bad_response" {
		t.Fatalf("probe of a broken yt-dlp: %q", got)
	}
}

// regression: music_play's relay fetched the url before its available_at and youtube answered 403
func TestResolveWaitsUntilYouTubeServesTheAudio(t *testing.T) {
	const availableAt = 1800000000
	for _, c := range []struct {
		name     string
		body     string
		min, max time.Duration
	}{
		{"before available_at it waits", "echo 10; echo " + strconv.Itoa(availableAt) + "; echo '" + streamURL + "'", 250 * time.Millisecond, 3 * time.Second},
		{"NA at once", "echo 10; echo NA; echo '" + streamURL + "'", 0, 250 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin, dir := fakeBin(t, c.body)
			now := time.Unix(availableAt, 0).Add(-300 * time.Millisecond)
			var log bytes.Buffer
			p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&log), Now: func() time.Time { return now }})
			start := time.Now()
			s, err := p.Resolve(context.Background(), "5qZQEq_C3vc")
			if err != nil {
				t.Fatal(err)
			}
			if took := time.Since(start); took < c.min || took > c.max || s.URL != streamURL {
				t.Fatalf("took %v, stream %+v", took, s)
			}
			args, _ := os.ReadFile(filepath.Join(dir, "args"))
			if !strings.Contains(string(args), "%(available_at)s") {
				t.Fatalf("args lack available_at:\n%s", args)
			}
		})
	}
}
