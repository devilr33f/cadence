package install

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// scripted answers each script with the first reply whose key it contains, and records what ran and was pushed
type scripted struct {
	replies map[string]Result
	ran     []string
	pushed  map[string]string
	locals  []string
}

func (s *scripted) Run(_ context.Context, script string) (Result, error) {
	s.ran = append(s.ran, script)
	keys := make([]string, 0, len(s.replies))
	for k := range s.replies {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if strings.Contains(script, k) {
			return s.replies[k], nil
		}
	}
	return Result{}, nil
}

func (s *scripted) Push(_ context.Context, local, path string) error {
	b, err := os.ReadFile(local) //nolint:gosec // the shell's own temp file
	if err != nil {
		return err
	}
	if s.pushed == nil {
		s.pushed = map[string]string{}
	}
	s.pushed[path] = string(b)
	s.locals = append(s.locals, local)
	return nil
}

func (s *scripted) String() string { return "scripted" }

func shellOver(r Runner, dry bool, out *bytes.Buffer) *Shell {
	return NewShell(r, ShellOptions{DataRoot: "/data/cadence", Work: "/data/cadence/.install", Inittab: "/etc/inittab", DryRun: dry, Out: out})
}

func TestShellReads(t *testing.T) {
	r := &scripted{replies: map[string]Result{
		"od -An":       {Out: []byte(" 61 0a 00\n 62\n")},
		"sha256sum <":  {Out: []byte(strings.Repeat("ab", 32) + "  -\n")},
		"readlink -f":  {Out: []byte("/data/cadence\n")},
		"for f in .*":  {Out: []byte(".\n..\nbin\nconfig.json\n")},
		"/proc":        {Out: []byte("proc 1\nexe /bin/busybox\narg init\nproc 2\nproc 33\nexe /data/cadence/cadenced\narg /data/cadence/cadenced\narg --config\narg /data/cadence/config.json\ntr: can't open\n")},
		"df -Pk":       {Out: []byte("12345\n")},
		"command -v":   {Out: []byte("/bin/tar\nmissing amixer\n")},
		"get 'Master'": {Out: []byte("Simple mixer control 'Master',0\n  Limits: Playback 0 - 255\n  Front Left: Playback 115 [45%] [on]\n  Front Right: Playback 117 [46%] [on]\n")},
		" level":       {Out: []byte("45\n")},
	}}
	s := shellOver(r, false, nil)
	ctx := context.Background()
	if b, ok, err := s.Read(ctx, "/etc/inittab"); err != nil || !ok || string(b) != "a\n\x00b" {
		t.Fatalf("read %q %v %v", b, ok, err)
	}
	if got, _, _ := s.Sum(ctx, "/x"); got != strings.Repeat("ab", 32) {
		t.Fatalf("sum %q", got)
	}
	if got, _ := s.List(ctx, "/data/cadence"); !slices.Equal(got, []string{"bin", "config.json"}) {
		t.Fatalf("list %q", got)
	}
	procs, _ := s.Processes(ctx)
	if len(procs) != 3 || procs[0].Exe != "/bin/busybox" || procs[1].Exe != "" || len(procs[1].Args) != 0 ||
		!slices.Equal(procs[2].Args, []string{"/data/cadence/cadenced", "--config", "/data/cadence/config.json"}) {
		t.Fatalf("procs %+v", procs)
	}
	if kb, _ := s.FreeKB(ctx, "/data"); kb != 12345 {
		t.Fatalf("free %d", kb)
	}
	if m, _ := s.Missing(ctx, []string{"tar", "amixer"}); !slices.Equal(m, []string{"amixer"}) {
		t.Fatalf("missing %q", m)
	}
	if m, _ := s.Mixer(ctx, "Master"); !slices.Equal(m, []int{45, 46}) {
		t.Fatalf("mixer %v", m)
	}
	if l, _ := s.Level(ctx, "/data/cadence/bin/cadence-run"); l != 45 {
		t.Fatalf("level %d", l)
	}
	r.replies = map[string]Result{"": {Code: 3}}
	if _, ok, err := s.Read(ctx, "/nope"); ok || err != nil {
		t.Fatalf("missing read %v %v", ok, err)
	}
	if got, err := s.List(ctx, "/nope"); got != nil || err != nil {
		t.Fatalf("missing list %v %v", got, err)
	}
	r.replies = map[string]Result{"": {Out: []byte("zz\n")}}
	if _, _, err := s.Read(ctx, "/x"); err == nil {
		t.Fatal("garbage read back")
	}
}

// a mono volume amixer gives no direction (the vk capsula's TV-Set, read on the device) prints its level without
// "Playback"; a capture level is never taken for one
func TestShellMixerReadsAVolumeWithoutDirection(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		out  string
		want []int
	}{
		{"Simple mixer control 'TV-Set',0\n  Capabilities: volume volume-joined\n  Playback channels: Mono\n" +
			"  Capture channels: Mono\n  Limits: 0 - 100\n  Mono: 17 [17%] [-106.24dB]\n", []int{17}},
		{"Simple mixer control 'Mic',0\n  Capabilities: cvolume cvolume-joined\n  Capture channels: Mono\n" +
			"  Limits: Capture 0 - 31\n  Mono: Capture 20 [65%] [on]\n", nil},
		{"Simple mixer control 'PCM',0\n  Limits: Playback -40 - 0\n  Mono: Playback -10 [75%] [on] Capture 3 [10%]\n", []int{75}},
	} {
		s := shellOver(&scripted{replies: map[string]Result{"get ": {Out: []byte(tc.out)}}}, false, nil)
		if got, err := s.Mixer(ctx, "TV-Set"); err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%v %v, want %v, from:\n%s", got, err, tc.want, tc.out)
		}
	}
}

func TestShellRemovesOnlyUnderTheDataRoot(t *testing.T) {
	r := &scripted{}
	s := shellOver(r, false, nil)
	for _, p := range []string{"/", "/data", "/data/cadence", "/data/cadence/../etc", "/data/cadencex/y", "/etc/inittab"} {
		if err := s.Remove(context.Background(), p); err == nil {
			t.Errorf("removed %s", p)
		}
	}
	if len(r.ran) != 0 {
		t.Fatalf("ran %q", r.ran)
	}
	if err := s.Remove(context.Background(), "/data/cadence/.install/tree"); err != nil || r.ran[0] != "rm -rf '/data/cadence/.install/tree'" {
		t.Fatalf("%v %q", err, r.ran)
	}
}

// the code reaches the device only inside a pushed file, mode 600, and the local copy goes; no command and no
// printed line holds it
func TestShellKeepsTheCodeOutOfCommands(t *testing.T) {
	const code = "SECRET-CODE-1234"
	r := &scripted{}
	var out bytes.Buffer
	s := shellOver(r, false, &out)
	ctx := context.Background()
	if err := s.WriteSecret(ctx, "/data/cadence/.install/code", []byte(code)); err != nil {
		t.Fatal(err)
	}
	if err := s.Enroll(ctx, Enrollment{Daemon: "/data/cadence/bin/cadenced", Server: "https://h", CodeFile: "/data/cadence/.install/code",
		Board: "yandex-pickle", DataDir: "/data/cadence", CAFile: "/data/cadence/share/ca-certificates.crt", Config: "/data/cadence/config.json"}); err != nil {
		t.Fatal(err)
	}
	if r.pushed["/data/cadence/.install/.code.cadence-new"] != code {
		t.Fatalf("pushed %q", r.pushed)
	}
	want := []string{
		"chmod 600 '/data/cadence/.install/.code.cadence-new' && mv -f '/data/cadence/.install/.code.cadence-new' '/data/cadence/.install/code'",
		`'/data/cadence/bin/cadenced' enroll --server 'https://h' --code "$(cat '/data/cadence/.install/code')" --board 'yandex-pickle' --audiod --data-dir '/data/cadence' --ca-file '/data/cadence/share/ca-certificates.crt' --config '/data/cadence/config.json'`,
	}
	if !slices.Equal(r.ran, want) {
		t.Fatalf("ran:\n%s", strings.Join(r.ran, "\n"))
	}
	if _, err := os.Stat(r.locals[0]); !os.IsNotExist(err) {
		t.Fatal("the local copy of the code stayed")
	}
	r.replies = map[string]Result{"enroll": {Code: 3, Out: []byte("cadenced enroll: code refused")}}
	if err := s.Enroll(ctx, Enrollment{}); !errors.Is(err, ErrEnrollRefused) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out.String()+strings.Join(r.ran, ""), code) {
		t.Fatal("the code is in a command or the output")
	}
}

// a dry run runs the reads and prints every change, the code redacted
func TestShellDryRun(t *testing.T) {
	r := &scripted{replies: map[string]Result{"od -An": {Out: []byte("61\n")}, "readlink -f": {Out: []byte("/data/cadence\n")}}}
	var out bytes.Buffer
	s := shellOver(r, true, &out)
	ctx := context.Background()
	if b, ok, _ := s.Read(ctx, "/etc/inittab"); !ok || string(b) != "a" {
		t.Fatal("a dry run did not read")
	}
	_ = s.WriteSecret(ctx, "/data/cadence/.install/code", []byte("SECRET-CODE-1234"))
	_ = s.Write(ctx, "/data/cadence/state/volume", []byte(`{"level":45}`), 0o600)
	_ = s.Rename(ctx, "/data/cadence/cadenced", "/data/cadence/v1/cadenced")
	_ = s.SwapInittab(ctx, "/data/cadence/.install/inittab.next", "old", "new")
	_, _ = s.Signal(ctx, Proc{Pid: 7, Exe: "/data/cadence/cadenced", Args: []string{"/data/cadence/cadenced"}}, "TERM")
	_ = s.Push(ctx, "local.tar", "/data/cadence/.install/cadence.tar")
	_ = s.Enroll(ctx, Enrollment{Daemon: "/data/cadence/bin/cadenced", CodeFile: "/data/cadence/.install/code"})
	// the reads: the inittab and the data root's resolved path
	if len(r.ran) != 2 || len(r.pushed) != 0 {
		t.Fatalf("a dry run changed the device: %q %q", r.ran, r.pushed)
	}
	printed := out.String()
	for _, want := range []string{
		"would write /data/cadence/.install/code (redacted, mode 600)",
		"would write /data/cadence/state/volume (12 bytes, mode 600)",
		"would run: if [ -e '/data/cadence/v1/cadenced' ] || [ -L '/data/cadence/v1/cadenced' ]; then exit 4; fi; mv '/data/cadence/cadenced' '/data/cadence/v1/cadenced'",
		"would replace /etc/inittab with /data/cadence/.install/inittab.next (sha256 new)",
		"'kill' -TERM 7",
		"would push local.tar to /data/cadence/.install/cadence.tar",
		`--code "$(cat '/data/cadence/.install/code')"`,
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("dry run lacks %q:\n%s", want, printed)
		}
	}
	if strings.Contains(printed, "SECRET-CODE-1234") {
		t.Fatal("a dry run printed the code")
	}
}
