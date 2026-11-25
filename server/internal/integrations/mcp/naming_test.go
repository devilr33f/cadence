package mcp_test

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"regexp"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
)

func TestDerivePrefix(t *testing.T) {
	for in, want := range map[string]string{
		"github":                 "github",
		"Home.Assistant-2":       "home_assistant_2",
		"1st-server":             "st_server",
		"42":                     "srv",
		"_.-":                    "srv",
		"a-very-long-instance-x": "a_very_long_inst",
		"abcdefghijklmno.x":      "abcdefghijklmno",
		"Ünïcode":                "n_code",
	} {
		got := mcp.DerivePrefix(in)
		if got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`).MatchString(got) {
			t.Errorf("%q: %q is no tool prefix", in, got)
		}
	}
}

func hashed(head, tool, remote string) string {
	sum := sha256.Sum256([]byte(remote))
	return head + tool + "_" + hex.EncodeToString(sum[:])[:6]
}

func TestLocalNames(t *testing.T) {
	long := strings.Repeat("x", 64-len("mcp_p_"))
	got := mcp.AssignNames("p", []string{"echo", "search.web", "Grüße", "a-b_C", long, long + "y"}, nil, nil)
	want := map[string]string{
		"echo":       "mcp_p_echo",
		"search.web": "mcp_p_search_web",
		"Grüße":      "mcp_p_Gr__e",
		"a-b_C":      "mcp_p_a-b_C",
		// 64 exactly stays plain; one more is cut to fit its hash
		long:       "mcp_p_" + long,
		long + "y": hashed("mcp_p_", long[:64-len("mcp_p_")-7], long+"y"),
	}
	if !maps.Equal(got, want) {
		t.Fatalf("names %v\nwant %v", got, want)
	}
	for _, n := range got {
		if len(n) > 64 || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(n) {
			t.Fatalf("%q is no tool name", n)
		}
	}
}

func TestLocalNameCollisions(t *testing.T) {
	// a clean name keeps its plain name whatever the listing order; the one
	// sanitized onto it takes its hash
	for _, remotes := range [][]string{{"a.b", "a_b"}, {"a_b", "a.b"}} {
		got := mcp.AssignNames("p", remotes, nil, nil)
		if got["a_b"] != "mcp_p_a_b" || got["a.b"] != hashed("mcp_p_", "a_b", "a.b") {
			t.Fatalf("%v: %v", remotes, got)
		}
	}
	// a name stored for a remote stays its own when a clean one turns up
	got := mcp.AssignNames("p", []string{"a.b", "a_b"}, map[string]string{"a.b": "mcp_p_a_b"}, nil)
	if got["a.b"] != "mcp_p_a_b" || got["a_b"] != hashed("mcp_p_", "a_b", "a_b") {
		t.Fatalf("stored name moved: %v", got)
	}
	// a gone tool's stored name stays taken, so it never passes to another tool
	got = mcp.AssignNames("p", []string{"a_b"}, map[string]string{"a.b": "mcp_p_a_b"}, nil)
	if got["a_b"] != hashed("mcp_p_", "a_b", "a_b") {
		t.Fatalf("a gone tool's name was reused: %v", got)
	}
	// and keeps its hashed name once the plain one is free again
	got = mcp.AssignNames("p", []string{"a_b"}, map[string]string{"a_b": hashed("mcp_p_", "a_b", "a_b")}, nil)
	if got["a_b"] != hashed("mcp_p_", "a_b", "a_b") {
		t.Fatalf("a hashed name moved: %v", got)
	}
	// a stored name under an earlier prefix is named again
	got = mcp.AssignNames("q", []string{"echo"}, map[string]string{"echo": "mcp_p_echo"}, nil)
	if got["echo"] != "mcp_q_echo" {
		t.Fatalf("prefix change: %v", got)
	}
	// a clean name that spells another remote's hashed name leaves it none
	clash := hashed("mcp_p_", "a_b", "a.b")[len("mcp_p_"):]
	got = mcp.AssignNames("p", []string{"a.b", "a_b", clash}, nil, nil)
	if got[clash] != "mcp_p_"+clash || got["a_b"] != "mcp_p_a_b" || got["a.b"] != "" {
		t.Fatalf("hash clash: %v", got)
	}
}

// another instance's names are never handed out: prefixes that fold together
// (my-notes, my_notes) or overlap at _ (git + hub_search, git_hub + search)
func TestLocalNamesAvoidOtherInstances(t *testing.T) {
	taken := map[string]bool{"mcp_git_hub_search": true, hashed("mcp_git_hub_", "x", "x"): true}
	got := mcp.AssignNames("git_hub", []string{"search", "x", "free"}, nil, taken)
	want := map[string]string{"search": hashed("mcp_git_hub_", "search", "search"), "x": "mcp_git_hub_x", "free": "mcp_git_hub_free"}
	if !maps.Equal(got, want) {
		t.Fatalf("names %v\nwant %v", got, want)
	}
	taken["mcp_git_hub_x"] = true
	if got := mcp.AssignNames("git_hub", []string{"x"}, nil, taken); got["x"] != "" {
		t.Fatalf("both forms taken: %v", got)
	}
}
