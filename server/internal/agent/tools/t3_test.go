package tools_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code/t3codetest"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

type t3Rig struct {
	t      *testing.T
	fake   *t3codetest.Server
	byName map[string]agent.Tool
}

func newT3Rig(t *testing.T) *t3Rig {
	t.Helper()
	fake := t3codetest.New(t3codetest.Config{Token: "tok"})
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := t3code.NewClient(t3code.ClientConfig{BaseURL: srv.URL, Token: "tok", HTTP: srv.Client(), Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	src := t3code.NewSource(t3code.SourceDeps{})
	src.Apply([]t3code.Want{{Instance: "work", Client: c}})
	t.Cleanup(src.Close)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, ok := src.Shell("work"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no shell")
		}
		time.Sleep(5 * time.Millisecond)
	}
	r := &t3Rig{t: t, fake: fake, byName: map[string]agent.Tool{}}
	for _, tool := range tools.T3Code(tools.T3Deps{Source: src}) {
		r.byName[tool.Name] = tool
	}
	return r
}

func (r *t3Rig) call(name string, trig cadencev2.Trigger, args string) (json.RawMessage, *agent.ToolError) {
	r.t.Helper()
	tool, err := agent.Compile(r.byName[name])
	if err != nil {
		r.t.Fatal(err)
	}
	if e := agent.Validate(tool, json.RawMessage(args)); e != nil {
		return nil, e
	}
	return agent.Run(context.Background(), tool, agent.Call{DeviceID: "d", TurnID: "t", Trigger: trig, Args: json.RawMessage(args)})
}

func (r *t3Rig) last(typ string) string {
	cmds := r.fake.Commands()
	for i := len(cmds) - 1; i >= 0; i-- {
		if cmds[i].Type == typ {
			return string(cmds[i].Raw)
		}
	}
	return ""
}

func (r *t3Rig) waitStatus(thread, status string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := r.call("t3_threads", wokenTrig, `{"query":"`+thread+`"}`)
		if strings.Contains(string(out), `"status":"`+status+`"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("%s never %s", thread, status)
}

// the tools are offered only while the setting names an instance; the
// list taints, the write tools act in a woken session alone
func TestT3ToolsPolicy(t *testing.T) {
	r := newT3Rig(t)
	off, on := settings.Defaults(), settings.Defaults()
	on.T3Code = []string{"work"}
	for name, tool := range r.byName {
		if tool.Available == nil || tool.Available(off) || !tool.Available(on) {
			t.Errorf("%s offered without the setting", name)
		}
		write := name != "t3_threads"
		if write != (tool.Effects == agent.Write && tool.RequiresWokenSession && !tool.TaintExempt) {
			t.Errorf("%s policy %+v", name, tool)
		}
	}
	if r.byName["t3_threads"].Output != agent.Untrusted {
		t.Fatal("t3_threads must taint")
	}
	if _, e := r.call("t3_send", cadencev2.Trigger_TRIGGER_FOLLOW_UP, `{"thread":"ask","text":"ask x"}`); e == nil || e.Code != "wake_required" {
		t.Fatalf("follow-up %+v", e)
	}
}

// threads are named by id or title: one that fits several answers their ids
// and nothing is sent
func TestT3ThreadsAndNames(t *testing.T) {
	r := newT3Rig(t)
	out, e := r.call("t3_threads", wokenTrig, `{}`)
	if e != nil || !strings.Contains(string(out), `"id":"thread-ask"`) || !strings.Contains(string(out), `"title":"Cadence"`) {
		t.Fatalf("list %s %+v", out, e)
	}
	out, _ = r.call("t3_threads", wokenTrig, `{"query":"APP","project":"cadence"}`)
	if !strings.Contains(string(out), "thread-approve") || strings.Contains(string(out), "thread-ask") {
		t.Fatalf("filtered %s", out)
	}
	before := len(r.fake.Commands())
	_, e = r.call("t3_send", wokenTrig, `{"thread":"a","text":"finish it"}`)
	if e == nil || e.Code != "ambiguous" || len(e.Candidates) != 3 {
		t.Fatalf("ambiguous %+v", e)
	}
	for _, c := range e.Candidates {
		if !strings.HasPrefix(c, "thread-") {
			t.Fatalf("a candidate is not an id: %q", c)
		}
	}
	if len(r.fake.Commands()) != before {
		t.Fatal("an ambiguous name acted")
	}
	if _, e := r.call("t3_stop", wokenTrig, `{"thread":"nothing like it"}`); e == nil || e.Code != "not_found" {
		t.Fatalf("not found %+v", e)
	}
}

// send starts an idle thread, steers a running one; stop stops it; launch
// starts a thread on the project's model, approval-required
func TestT3SendStopLaunch(t *testing.T) {
	r := newT3Rig(t)
	if _, e := r.call("t3_send", wokenTrig, `{"thread":"hold","text":"hold on","mode":"steer"}`); e == nil || e.Code != "not_running" {
		t.Fatalf("steer idle %+v", e)
	}
	out, e := r.call("t3_send", wokenTrig, `{"thread":"hold","text":"hold on"}`)
	if e != nil || !strings.Contains(string(out), `"mode":"start_immediately"`) || !strings.Contains(r.last("message.dispatch"), `"text":"hold on"`) {
		t.Fatalf("send %s %+v", out, e)
	}
	r.waitStatus("hold", "running")
	if out, e := r.call("t3_send", wokenTrig, `{"thread":"thread-hold","text":"and this","mode":"steer"}`); e != nil || !strings.Contains(string(out), "steer_active") {
		t.Fatalf("steer %s %+v", out, e)
	}
	if _, e := r.call("t3_stop", wokenTrig, `{"thread":"hold"}`); e != nil || r.last("thread.stop") == "" {
		t.Fatalf("stop %+v", e)
	}
	r.waitStatus("hold", "cancelled")
	out, e = r.call("t3_launch", wokenTrig, `{"project":"Cadence","text":"finish the release notes","worktree":true}`)
	if e != nil || !strings.Contains(string(out), `"started":true`) {
		t.Fatalf("launch %s %+v", out, e)
	}
	raw := r.last("launchThread")
	for _, want := range []string{`"runtimeMode":"approval-required"`, `"model":"claude-opus-5-5"`, `"title":"finish the release notes"`, `"type":"worktree"`, `"baseRef":"HEAD"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("launch lacks %s: %s", want, raw)
		}
	}
	if _, e := r.call("t3_launch", wokenTrig, `{"project":"elsewhere","text":"x"}`); e == nil || e.Code != "not_found" {
		t.Fatalf("unknown project %+v", e)
	}
}
