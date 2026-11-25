package deviceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/opsplan"
)

// fakeTransport records commands and answers them from a script.
type fakeTransport struct {
	failAt  string // a step name whose exec fails
	hashes  map[string]string
	calls   []string
	written map[string][]byte
}

func (f *fakeTransport) probe(context.Context, access) error { return nil }

func (f *fakeTransport) exec(_ context.Context, _ access, cmd string, _ time.Duration) (int, string, error) {
	f.calls = append(f.calls, cmd)
	for _, name := range strings.Split(f.failAt, ",") {
		if name != "" && strings.Contains(cmd, name) {
			return 3, name + " failed", nil
		}
	}
	return 0, "", nil
}

func (f *fakeTransport) writeFile(_ context.Context, _ access, path string, _ uint32, data []byte) error {
	f.calls = append(f.calls, "write "+path)
	if f.written == nil {
		f.written = map[string][]byte{}
	}
	f.written[path] = data
	return nil
}

func (f *fakeTransport) readHash(_ context.Context, _ access, path string) (string, error) {
	b, ok := f.written[path]
	if !ok {
		return "", fmt.Errorf("no file")
	}
	if f.hashes != nil {
		if h, ok := f.hashes[path]; ok {
			return h, nil
		}
	}
	return hashOf(b), nil
}

func testService() *Service {
	return &Service{now: time.Now, log: slog.New(slog.DiscardHandler),
		hellos: map[string]hello{}, activity: map[string]time.Time{}}
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func testArtifact(files ...string) *artifact {
	a := &artifact{board: "yandex-midi", kind: "tar", version: "v9", data: []byte("tar")}
	for _, p := range files {
		a.files = append(a.files, opsplan.FileEntry{Path: p, SHA256: hashOf([]byte(p)), Size: 10, Mode: 0o755})
	}
	return a
}

func TestYandexProfileStagesSwapsAndRestarts(t *testing.T) {
	a := testArtifact("MANIFEST", "bin/cadenced", "bin/cadence-audiod")
	p, err := yandexHotswap(a, "run1")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "yandex-hotswap" || p.TargetVersion != "v9" || !p.Restart || !p.HasTerminal() {
		t.Fatalf("plan header %+v", p)
	}
	var stages, swaps int
	for i, st := range p.Steps {
		if st.Fetch != nil {
			stages++
			if st.Fetch.Dst != "/data/cadence/"+st.Name[len("stage:"):]+".new" {
				t.Errorf("step %d dst %s", i, st.Fetch.Dst)
			}
			if !st.Fetch.SkipIfSame {
				t.Errorf("step %d not skip-if-same", i)
			}
		}
		if st.Exec != nil && strings.HasPrefix(st.Name, "swap:") {
			swaps++
			rel := st.Name[len("swap:"):]
			if !strings.Contains(st.Exec.Cmd, "cp -p "+rel+" "+rel+".prev") ||
				!strings.Contains(st.Exec.Cmd, "mv "+rel+".new "+rel) ||
				!strings.Contains(st.Exec.Cmd, "sha256sum") {
				t.Errorf("swap %s: %s", rel, st.Exec.Cmd)
			}
		}
	}
	if stages != 3 || swaps != 3 {
		t.Fatalf("stages %d swaps %d", stages, swaps)
	}
	last := p.Steps[len(p.Steps)-1]
	if last.Exec == nil || !last.Exec.Terminal || !strings.Contains(last.Exec.Cmd, "pidof cadenced") {
		t.Fatalf("last step %+v", last)
	}
	if len(p.Rollback) != 4 || !p.Rollback[len(p.Rollback)-1].Exec.Terminal {
		t.Fatalf("rollback %+v", p.Rollback)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	// a board with only runtimes and no bin/ files is refused
	if _, err := yandexHotswap(testArtifact("MANIFEST"), "run1"); err == nil {
		t.Fatal("an artifact without bin/ files built a plan")
	}
}

func TestTVProfileUsesPmInstall(t *testing.T) {
	a := &artifact{board: "android-tv", kind: "apk", version: "v4", sha: strings.Repeat("b", 64), data: []byte("apk")}
	p, err := tvAPK(a, "run2")
	if err != nil {
		t.Fatal(err)
	}
	if p.Steps[1].Exec.Cmd != "pm install -r /data/local/tmp/cadence-update.apk" {
		t.Fatalf("install %s", p.Steps[1].Exec.Cmd)
	}
	a.downgrade = true
	p, err = tvAPK(a, "run2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Steps[1].Exec.Cmd, "-r -d ") {
		t.Fatalf("downgrade install %s", p.Steps[1].Exec.Cmd)
	}
	if _, err := profileFor("android-tv", "tar"); err == nil {
		t.Fatal("a tar on android-tv built a plan")
	}
	d := &artifact{board: "rpi", kind: "deb", version: "v5", sha: strings.Repeat("b", 64), data: []byte("deb")}
	rpi, err := rpiDeb(d, "run3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rpi.Steps[2].Exec.Cmd, "--allow-downgrades") || !rpi.Steps[5].Exec.Terminal {
		t.Fatalf("rpi install %s", rpi.Steps[2].Exec.Cmd)
	}
	if err := rpi.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := profileFor("sim", "tar"); err == nil {
		t.Fatal("sim has no update profile")
	}
	if p, err := profileFor("vk-capsula-mini", "tar"); err != nil || p.id != "yandex-hotswap" {
		t.Fatalf("vk capsula mini: %v %v", p.id, err)
	}
}

func TestRunStepsHappyAndFailure(t *testing.T) {
	s := testService()
	a := testArtifact("MANIFEST", "bin/cadenced")
	a.files[1].SHA256 = hashOf(a.data) // the fetch's read-back must match
	plan := &opsplan.Plan{ID: "p", RunID: "r", Board: "yandex-midi", TargetVersion: "v9",
		Steps: []opsplan.Step{
			{Name: "stage:bin/cadenced", Fetch: &opsplan.FetchStep{Src: "artifact:bin/cadenced", Dst: "/data/cadence/bin/cadenced.new",
				SHA256: hashOf(a.data), Mode: 0o755}},
			{Name: "swap", Exec: &opsplan.ExecStep{Cmd: "mv thing"}},
			{Name: "restart", Exec: &opsplan.ExecStep{Cmd: "kill", Terminal: true}},
		},
		Rollback: []opsplan.Step{{Name: "undo", Exec: &opsplan.ExecStep{Cmd: "mv back"}}},
	}
	// the artifact serves the member from its tar: patch open to answer the blob
	a.members = map[string][]byte{"bin/cadenced": a.data}
	r := runRec{board: "yandex-midi", plan: plan, artifact: a}
	tr := &fakeTransport{}
	out := make([]opsplan.StepResult, len(plan.Steps))
	restart, failed := s.runSteps(context.Background(), r, tr, access{deviceID: "d", addr: "1.2.3.4:5555"}, plan.Steps, out)
	if failed != nil || restart != 2 {
		t.Fatalf("restart %d failed %+v", restart, failed)
	}
	for i, res := range out {
		if res.State != opsplan.StateDone {
			t.Fatalf("step %d %+v", i, res)
		}
	}

	// an exec failure past staging rolls back
	tr = &fakeTransport{failAt: "mv thing"}
	out = make([]opsplan.StepResult, len(plan.Steps))
	restart, failed = s.runSteps(context.Background(), r, tr, access{deviceID: "d", addr: "1.2.3.4:5555"}, plan.Steps, out)
	if failed == nil || failed.Index != 1 || failed.Result.ErrorCode != opsplan.ErrExit {
		t.Fatalf("failed %+v", failed)
	}
	if restart != -1 {
		t.Fatalf("restart %d", restart)
	}
	if err := s.rollbackSteps(context.Background(), r, tr, access{deviceID: "d", addr: "1.2.3.4:5555"}, out); err != nil {
		t.Fatalf("rollback %v", err)
	}
	joined := strings.Join(tr.calls, "\n")
	if !strings.Contains(joined, "mv back") {
		t.Fatalf("rollback commands %v", tr.calls)
	}

	// a hash the device answers that differs aborts before any swap
	tr = &fakeTransport{hashes: map[string]string{"/data/cadence/bin/cadenced.new": strings.Repeat("c", 64)}}
	out = make([]opsplan.StepResult, len(plan.Steps))
	_, failed = s.runSteps(context.Background(), r, tr, access{deviceID: "d", addr: "1.2.3.4:5555"}, plan.Steps, out)
	if failed == nil || failed.Result.ErrorCode != opsplan.ErrHashMismatch {
		t.Fatalf("failed %+v", failed)
	}
}

func TestWrapUnwrapScript(t *testing.T) {
	code, body, ok := unwrapScript("before\r\nmore\r\ncadence-rc=7\r\n")
	if !ok || code != 7 || body != "before\nmore" {
		t.Fatalf("code %d body %q ok %v", code, body, ok)
	}
	if _, _, ok := unwrapScript("died mid command\n"); ok {
		t.Fatal("a session that died answered")
	}
}

func TestValidateAccess(t *testing.T) {
	for _, bad := range []string{"192.168.1.1", "host:port", "a b:5555"} {
		if err := validateAddr(TransportAdb, bad); err == nil {
			t.Errorf("adb accepted %q", bad)
		}
	}
	if err := validateAddr(TransportAdb, "192.168.1.156:5555"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "host name", "root@pi host"} {
		if err := validateAddr(TransportSSH, bad); err == nil {
			t.Errorf("ssh accepted %q", bad)
		}
	}
	if err := validateAddr(TransportSSH, "root@192.168.0.190:22"); err != nil {
		t.Fatal(err)
	}
}
