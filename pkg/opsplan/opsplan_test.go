package opsplan

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	p := &Plan{ID: "yandex-hotswap", RunID: "r1", Board: "yandex-midi", TargetVersion: "v9",
		Steps: []Step{
			{Name: "stage manifest", Fetch: &FetchStep{Src: "artifact:MANIFEST", Dst: "/data/cadence/MANIFEST.new",
				SHA256: strings.Repeat("a", 64)}},
			{Name: "restart", Exec: &ExecStep{Cmd: "kill $(pidof cadenced)", Terminal: true}},
		}}
	if err := p.Validate(); err != nil {
		t.Fatalf("valid plan: %v", err)
	}
	if !p.HasTerminal() {
		t.Fatal("plan has a terminal step")
	}
	for name, bad := range map[string]Plan{
		"both":    {Steps: []Step{{Name: "x", Fetch: &FetchStep{}, Exec: &ExecStep{}}}},
		"neither": {Steps: []Step{{Name: "x"}}},
		"src":     {Steps: []Step{{Name: "x", Fetch: &FetchStep{Src: "http://x", Dst: "/a", SHA256: strings.Repeat("a", 64)}}}},
		"dst":     {Steps: []Step{{Name: "x", Fetch: &FetchStep{Src: "artifact:", Dst: "a", SHA256: strings.Repeat("a", 64)}}}},
		"hash":    {Steps: []Step{{Name: "x", Fetch: &FetchStep{Src: "artifact:", Dst: "/a", SHA256: "nope"}}}},
		"name":    {Steps: []Step{{Exec: &ExecStep{}}}},
	} {
		if bad.ID == "" {
			bad.ID, bad.RunID, bad.Board, bad.TargetVersion = "p", "r", "b", "v"
		}
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte("# cadence 9a0b2618be25\n# board yandex-midi\n" +
		strings.Repeat("0", 64) + "  bin/cadenced\n" + strings.Repeat("1", 64) + "  bin/lib/libcrypto.so.3\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Version() != "9a0b2618be25" || m.Headers["board"] != "yandex-midi" {
		t.Fatalf("headers: %+v", m.Headers)
	}
	if len(m.Files) != 2 || m.Files[0].Path != "bin/cadenced" {
		t.Fatalf("files: %+v", m.Files)
	}
	for name, bad := range map[string]string{
		"version": "sha  file\n",
		"line":    "# cadence v\nnot-a-sum  file\n",
		"empty":   "# cadence v\n",
	} {
		if _, err := ParseManifest([]byte(bad)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestCleanTail(t *testing.T) {
	if got := CleanTail([]byte("a\r\nb\r\nc\r\n"), 1024); got != "a\nb\nc" {
		t.Fatalf("tail: %q", got)
	}
	long := strings.Repeat("x", 3000)
	if got := CleanTail([]byte(long), 100); len(got) != 100 {
		t.Fatalf("cut: %d", len(got))
	}
	two := strings.Repeat("y", 90) + "\n" + strings.Repeat("z", 90)
	if got := CleanTail([]byte(two), 100); strings.HasPrefix(got, "y") || len(got) != 90 {
		t.Fatalf("line cut: %d %q", len(got), got[:8])
	}
}
