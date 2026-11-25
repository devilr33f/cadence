package mcp_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
)

// toolRig is a catalog running one fake server, srv, and the source the
// agent reads it through.
type toolRig struct {
	*catRig
	f   *mcptest.Server
	srv mcp.Server
	src *mcp.Source
}

func newTools(t *testing.T, o mcptest.Options, opts ...clientOpt) *toolRig {
	t.Helper()
	r := newCatalog(t)
	f := fake(t, o)
	return &toolRig{
		catRig: r, f: f, srv: r.server("srv", f, opts...),
		src: mcp.NewSource(r.cat, slog.New(slog.NewTextHandler(r.logs, nil))),
	}
}

func withMaxResult(n int) clientOpt { return func(c *mcp.ClientConfig) { c.MaxResultChars = n } }

// start runs srv and waits for its listing to hold remote.
func (r *toolRig) start(remote string) {
	r.t.Helper()
	r.cat.Apply([]mcp.Server{r.srv})
	r.listed("srv", remote)
}

// tool is the source's tool of that local name as a turn starting now gets it.
func (r *toolRig) tool(local string) agent.Tool {
	r.t.Helper()
	return find(r.t, r.src.Tools(), local)
}

func find(t *testing.T, tools []agent.Tool, name string) agent.Tool {
	t.Helper()
	i := slices.IndexFunc(tools, func(tl agent.Tool) bool { return tl.Name == name })
	if i < 0 {
		t.Fatalf("no tool %s in %v", name, toolNamesOf(tools))
	}
	return tools[i]
}

func toolNamesOf(tools []agent.Tool) []string {
	out := []string{}
	for _, tl := range tools {
		out = append(out, tl.Name)
	}
	return out
}

// set applies an override to srv's remote tool.
func (r *toolRig) set(remote string, p mcp.Patch) {
	r.t.Helper()
	if _, err := r.cat.Override(bg(), "srv", remote, p); err != nil {
		r.t.Fatal(err)
	}
}

func str(s string) *string { return &s }
func yes() *bool           { v := true; return &v }

var (
	asRead    = mcp.Patch{Effects: str(mcp.EffectsRead)}
	asTrusted = mcp.Patch{Trust: str(mcp.TrustTrusted)}
)

// call runs the tool as the agent does, in a woken turn.
func (r *toolRig) call(tl agent.Tool, args string) (json.RawMessage, *agent.ToolError) {
	return r.callIn(bg(), tl, args)
}

func (r *toolRig) callIn(ctx context.Context, tl agent.Tool, args string) (json.RawMessage, *agent.ToolError) {
	return agent.Run(ctx, tl, agent.Call{
		DeviceID: "dev-1", SessionID: "s-1", TurnID: "turn-1", Trigger: cadencev2.Trigger_TRIGGER_WAKE, Args: json.RawMessage(args),
	})
}

// failed checks that the call failed with code, retryable as said, in fixed
// words that carry none of the server's text.
func failed(t *testing.T, e *agent.ToolError, code string, retryable bool) {
	t.Helper()
	if e == nil || e.Code != code || e.Retryable != retryable {
		t.Fatalf("error %+v, want %s retryable=%v", e, code, retryable)
	}
	for _, s := range []string{e.Message, e.Hint} {
		if strings.Contains(s, mcptest.Leak) || strings.Contains(s, auth) {
			t.Fatalf("error %+v leaks", e)
		}
	}
	if e.Message == "" {
		t.Fatalf("error %+v has no message", e)
	}
}

var callLine = regexp.MustCompile(`msg=mcp_call instance=srv tool=(\S+) code=(\S+) ms=\d+ bytes=(\d+)\n`)

// calls is every mcp_call line logged, as tool/code/bytes.
func (r *toolRig) calls() []string {
	var out []string
	for _, m := range callLine.FindAllStringSubmatch(r.logs.String(), -1) {
		out = append(out, m[1]+"/"+m[2]+"/"+m[3])
	}
	return out
}

func textResult(s string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}
}

const anyObject = `{"type":"object"}`
