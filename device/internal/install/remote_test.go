package install

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWrapAndUnwrap(t *testing.T) {
	for out, want := range map[string]Result{
		"foo\n\ncadence-rc=0\n":          {Out: []byte("foo\n"), Code: 0},
		"foo\ncadence-rc=3\n":            {Out: []byte("foo"), Code: 3},
		"\ncadence-rc=1\n":               {Out: []byte{}, Code: 1},
		"a\r\nb\r\n\r\ncadence-rc=0\r\n": {Out: []byte("a\nb\n"), Code: 0},
	} {
		got, ok := unwrap([]byte(out))
		if !ok || string(got.Out) != string(want.Out) || got.Code != want.Code {
			t.Errorf("%q = %q %d %v", out, got.Out, got.Code, ok)
		}
	}
	for _, lost := range []string{"", "foo\n", "foocadence-rc=0\n", "x\ncadence-rc=\n", "x\ncadence-rc=0 extra\n"} {
		if _, ok := unwrap([]byte(lost)); ok {
			t.Errorf("%q read as a status", lost)
		}
	}
	if got := wrap("exit 4"); got != "( exit 4 ); rc=$?; echo; echo cadence-rc=$rc" {
		t.Fatal(got)
	}
	if got := quote("it's"); got != `'it'\''s'` {
		t.Fatal(got)
	}
}

// the wrapped command runs in a real sh where there is one: its own exit still reports, output intact
func TestWrapInSh(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	out, _ := exec.Command(sh, "-c", wrap("printf 'a b'; exit 7")).Output() //nolint:gosec // the test's own command
	res, ok := unwrap(out)
	if !ok || res.Code != 7 || string(res.Out) != "a b" {
		t.Fatalf("%q -> %q %d %v", out, res.Out, res.Code, ok)
	}
	q := quote(`a'b"$c`)
	out, _ = exec.Command(sh, "-c", "printf %s "+q).Output() //nolint:gosec // the test's own command
	if string(out) != `a'b"$c` {
		t.Fatalf("quote: %q", out)
	}
}

type call struct {
	name  string
	args  []string
	env   []string
	stdin string
}

func recorder(calls *[]call, stdout, stderr string, err error) execFunc {
	return func(_ context.Context, name string, args []string, env []string, stdin io.Reader) ([]byte, []byte, error) {
		c := call{name: name, args: args, env: env}
		if stdin != nil {
			b, _ := io.ReadAll(stdin)
			c.stdin = string(b)
		}
		*calls = append(*calls, c)
		return []byte(stdout), []byte(stderr), err
	}
}

func TestSSHNeverTrustsANewHostKey(t *testing.T) {
	var calls []call
	s := &SSH{Target: "root@192.0.2.1", Key: `C:\keys\owner`, KnownHosts: filepath.Join("/", "home", "o w", ".ssh", "known_hosts"), exec: recorder(&calls, "x\n\ncadence-rc=0\n", "", nil)}
	res, err := s.Run(context.Background(), "true")
	if err != nil || res.Code != 0 || string(res.Out) != "x\n" {
		t.Fatalf("%+v %v", res, err)
	}
	want := []string{"-T", "-i", `C:\keys\owner`, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", `UserKnownHostsFile="/home/o w/.ssh/known_hosts"`, "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=4", "--", "root@192.0.2.1", wrap("true")}
	if calls[0].name != "ssh" || !slices.Equal(calls[0].args, want) {
		t.Fatalf("ssh %q", calls[0].args)
	}

	local := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(local, []byte("payload\x00\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Push(context.Background(), local, "/data/cadence/.install/x"); err != nil {
		t.Fatal(err)
	}
	if c := calls[1]; c.args[len(c.args)-1] != wrap("cat > '/data/cadence/.install/x'") || c.stdin != "payload\x00\r\n" {
		t.Fatalf("push %q with %q", c.args, c.stdin)
	}
}

func TestSSHReportsItsOwnFailure(t *testing.T) {
	var calls []call
	s := &SSH{Target: "root@192.0.2.1", exec: recorder(&calls, "", "Warning: x\r\nHost key verification failed.\r\n", errors.New("exit status 255"))}
	if _, err := s.Run(context.Background(), "true"); err == nil || err.Error() != "ssh root@192.0.2.1: Host key verification failed." {
		t.Fatalf("err = %v", err)
	}
	s.exec = recorder(&calls, "partial", "", nil)
	if _, err := s.Run(context.Background(), "true"); err == nil || !strings.Contains(err.Error(), "the session ended before the command did") {
		t.Fatalf("err = %v", err)
	}
	s.exec = recorder(&calls, "\ncadence-rc=1\n", "", errors.New("exit status 1"))
	if err := s.Push(context.Background(), os.DevNull, "/x"); err == nil || !strings.Contains(err.Error(), "status 1") {
		t.Fatalf("err = %v", err)
	}
}

// adbd has no shell protocol: adb exits 0 whatever ran, so only the marker tells
func TestADBReadsTheMarker(t *testing.T) {
	var calls []call
	a := &ADB{Serial: "192.0.2.1:5555", exec: recorder(&calls, "out\r\n\r\ncadence-rc=2\r\n", "", nil)}
	res, err := a.Run(context.Background(), "false")
	if err != nil || res.Code != 2 || string(res.Out) != "out\n" {
		t.Fatalf("%+v %v", res, err)
	}
	if want := []string{"-s", "192.0.2.1:5555", "shell", wrap("false")}; calls[0].name != "adb" || !slices.Equal(calls[0].args, want) {
		t.Fatalf("adb %q", calls[0].args)
	}
	a.exec = recorder(&calls, "", "", nil)
	if _, err := a.Run(context.Background(), "true"); err == nil {
		t.Fatal("a session with no marker succeeded")
	}
	a.exec = recorder(&calls, "", "adb: error: device '192.0.2.1:5555' not found\n", errors.New("exit status 1"))
	if err := a.Push(context.Background(), "local", "/remote"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
	if c := calls[len(calls)-1]; !slices.Equal(c.args, []string{"-s", "192.0.2.1:5555", "push", "local", "/remote"}) {
		t.Fatalf("push %q", c.args)
	}
	a.Connect(context.Background())
	if c := calls[len(calls)-1]; !slices.Equal(c.args, []string{"connect", "192.0.2.1:5555"}) {
		t.Fatalf("connect %q", c.args)
	}
}

func TestAnswering(t *testing.T) {
	var calls []call
	dead := &SSH{Target: "root@192.0.2.1", exec: recorder(&calls, "", "Connection timed out\n", errors.New("exit status 255"))}
	alive := &ADB{Serial: "192.0.2.1:5555", exec: recorder(&calls, "\ncadence-rc=0\n", "", nil)}
	ok, errs := Answering(context.Background(), []Runner{dead, alive})
	if len(ok) != 1 || ok[0] != alive || len(errs) != 1 || !strings.Contains(errs[0].Error(), "Connection timed out") {
		t.Fatalf("%v %v", ok, errs)
	}
}
