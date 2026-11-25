package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

// cmdProvider lists cmds and answers every command through run.
type cmdProvider struct {
	*fakeProvider
	cmds map[string]string
	run  func(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)

	mu     sync.Mutex
	called []string
}

func newCmdProvider(id string, run func(context.Context, string, json.RawMessage) (json.RawMessage, error), names ...string) *cmdProvider {
	cmds := map[string]string{}
	for _, n := range names {
		cmds[n] = `{"type":"object"}`
	}
	return &cmdProvider{fakeProvider: newProvider(id), cmds: cmds, run: run}
}

func (p *cmdProvider) Descriptor() capability.Descriptor {
	d := p.fakeProvider.Descriptor()
	d.Commands = p.cmds
	return d
}

func (p *cmdProvider) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	p.called = append(p.called, name)
	p.mu.Unlock()
	return p.run(ctx, name, args)
}

func (p *cmdProvider) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.called...)
}

func (p *peer) command(id, capability, name, args string) {
	p.t.Helper()
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityCommand{CapabilityCommand: &cadencev2.CapabilityCommand{
		RequestId: id, Capability: capability, Name: name, Json: args,
	}}})
}

func (p *peer) result() *cadencev2.CommandResult {
	p.t.Helper()
	env := p.recv()
	r := env.GetCommandResult()
	if r == nil {
		p.t.Fatalf("frame = %v, want a command result", env)
	}
	return r
}

func TestCommandCodes(t *testing.T) {
	pr := newCmdProvider("media.test", func(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
		switch name {
		case "ok":
			if string(args) != `{"key":"s3cr3t-arg"}` {
				return nil, fmt.Errorf("args = %s", args)
			}
			return json.RawMessage(`{"value":"s3cr3t-result"}`), nil
		case "empty":
			return nil, nil
		case "unsupported":
			return nil, capability.ErrUnsupported
		case "coded":
			return nil, fmt.Errorf("wrapped: %w", &capability.CommandError{Code: "no_session"})
		case "badcode":
			return nil, &capability.CommandError{Code: "Not A Token"}
		case "longcode":
			return nil, &capability.CommandError{Code: strings.Repeat("x", 33)}
		default:
			return nil, errors.New("driver said s3cr3t-arg is wrong")
		}
	}, "ok", "empty", "unsupported", "coded", "badcode", "longcode", "broken")
	ls, buf := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, openStore(t))
	p := ls.accept(t)
	p.handshake(false)

	for i, c := range []struct {
		capability, name, code, json string
	}{
		{"media.test", "ok", "", `{"value":"s3cr3t-result"}`},
		{"media.test", "empty", "", ""},
		{"media.test", "unsupported", "unsupported", ""},
		{"media.test", "coded", "no_session", ""},
		{"media.test", "badcode", "failed", ""},
		{"media.test", "longcode", "failed", ""},
		{"media.test", "broken", "failed", ""},
		{"media.test", "hidden", "unknown_command", ""},
		{"c.unknown", "ok", "unknown_capability", ""},
	} {
		id := fmt.Sprintf("req-%d", i)
		p.command(id, c.capability, c.name, `{"key":"s3cr3t-arg"}`)
		if r := p.result(); r.GetRequestId() != id || r.GetCode() != c.code || r.GetJson() != c.json {
			t.Fatalf("%s.%s: result = %v", c.capability, c.name, r)
		}
	}
	if got := pr.calls(); strings.Join(got, ",") != "ok,empty,unsupported,coded,badcode,longcode,broken" {
		t.Fatalf("provider calls = %v", got)
	}
	out := buf.String()
	for _, want := range []string{
		`"msg":"command","capability":"media.test","name":"ok","code":"ok","ms":`,
		`"msg":"command","capability":"media.test","name":"coded","code":"no_session","ms":`,
		`"msg":"command","capability":"c.unknown","name":"ok","code":"unknown_capability","ms":`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s3cr3t") || strings.Contains(out, "driver said") {
		t.Fatalf("log carries args, results or upstream text: %s", out)
	}
}

func TestCommandTimesOut(t *testing.T) {
	pr := newCmdProvider("media.test", func(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, "slow")
	ls, _ := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, openStore(t),
		func(s *core.Sync) { core.SetCommandTimeout(s, 50*time.Millisecond) })
	p := ls.accept(t)
	p.handshake(false)
	p.command("r1", "media.test", "slow", `{}`)
	if r := p.result(); r.GetRequestId() != "r1" || r.GetCode() != "timeout" {
		t.Fatalf("result = %v", r)
	}
}

func TestCommandsBusyPastEight(t *testing.T) {
	started, release := make(chan struct{}, 16), make(chan struct{})
	pr := newCmdProvider("media.test", func(_ context.Context, name string, _ json.RawMessage) (json.RawMessage, error) {
		if name == "hold" {
			started <- struct{}{}
			<-release
		}
		return nil, nil
	}, "hold", "quick")
	ls, _ := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	for i := range 8 {
		p.command(fmt.Sprintf("h%d", i), "media.test", "hold", `{}`)
	}
	for range 8 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("held commands did not start")
		}
	}
	p.command("over", "media.test", "quick", `{}`)
	if r := p.result(); r.GetRequestId() != "over" || r.GetCode() != "busy" {
		t.Fatalf("ninth result = %v", r)
	}
	close(release)
	seen := map[string]bool{}
	for range 8 {
		r := p.result()
		if r.GetCode() != "" || !strings.HasPrefix(r.GetRequestId(), "h") {
			t.Fatalf("held result = %v", r)
		}
		seen[r.GetRequestId()] = true
	}
	if len(seen) != 8 {
		t.Fatalf("results for %v", seen)
	}
	p.command("after", "media.test", "quick", `{}`)
	if r := p.result(); r.GetRequestId() != "after" || r.GetCode() != "" {
		t.Fatalf("result once slots freed = %v", r)
	}
}

func TestCommandResultDroppedWhenLinkGone(t *testing.T) {
	started := make(chan struct{}, 1)
	pr := newCmdProvider("media.test", func(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
		started <- struct{}{}
		// finishes its work only after the link went away
		<-ctx.Done()
		return json.RawMessage(`{"late":true}`), nil
	}, "late")
	ls, buf := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	p.command("r1", "media.test", "late", `{}`)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("command did not start")
	}
	_ = p.c.Close(websocket.StatusGoingAway, "restart")

	p = ls.accept(t)
	p.handshake(false)
	// the late result is bound to the old session and can never come here; the
	// log shows that it was dropped rather than sent
	if a := p.configSet("c.unknown", 1, `{}`); a.GetCapability() != "c.unknown" {
		t.Fatalf("applied = %v", a)
	}
	if out := buf.String(); !strings.Contains(out, `"msg":"command","capability":"media.test","name":"late","code":"ok","ms":`) ||
		!strings.Contains(out, `"dropped":true`) {
		t.Fatalf("log: %s", out)
	}
}

// a result too large for a frame would never arrive: the server learns it failed
func TestOversizeResultAnsweredFailed(t *testing.T) {
	big := json.RawMessage(`{"pad":"` + strings.Repeat("x", wire.MaxFrameBytes) + `"}`)
	pr := newCmdProvider("media.test", func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return big, nil
	}, "big")
	ls, buf := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	p.command("r1", "media.test", "big", `{}`)
	if r := p.result(); r.GetRequestId() != "r1" || r.GetCode() != "failed" || r.GetJson() != "" {
		t.Fatalf("result = %v", r)
	}
	if out := buf.String(); !strings.Contains(out, `"msg":"command","capability":"media.test","name":"big","code":"failed","ms":`) {
		t.Fatalf("log: %s", out)
	}
}
