package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

// availBoard is a board whose capabilities come and go (board.Availabilities)
type availBoard struct {
	fakeBoard
	avail *capability.Availability
}

func (b availBoard) Availability() *capability.Availability { return b.avail }

func TestAvailabilityRidesOnStateDeltas(t *testing.T) {
	pr := newCmdProvider("display.screen", func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	}, "wake")
	pr.publish(`{"power":"sleep"}`)
	avail := capability.NewAvailability()
	avail.Set(false, "display.screen")
	ls, buf := startSync(t, availBoard{fakeBoard{id: "fake", caps: []capability.Provider{pr}}, avail}, openStore(t))
	delta := func(p *peer, json string, unavailable bool) {
		t.Helper()
		d := p.recv().GetStateDelta()
		if d.GetCapability() != "display.screen" || d.GetJson() != json || d.GetUnavailable() != unavailable {
			t.Fatalf("delta = %v, want %s unavailable=%v", d, json, unavailable)
		}
	}
	p := ls.accept(t)
	p.handshake(false)
	// the first delta of the link says it is down, with the state it last had
	delta(p, `{"power":"sleep"}`, true)

	// a command meets the absent hardware: refused, never run
	p.command("r1", "display.screen", "wake", `{}`)
	if r := p.result(); r.GetCode() != "unavailable" {
		t.Fatalf("result = %v, want unavailable", r)
	}
	if c := pr.calls(); len(c) != 0 {
		t.Fatalf("provider ran %v while unavailable", c)
	}

	// plugged in: the same state again, available; a repeat Set sends nothing
	avail.Set(true, "display.screen")
	delta(p, `{"power":"sleep"}`, false)
	if avail.Set(true, "display.screen") {
		t.Fatal("a repeat set changed")
	}
	pr.publish(`{"power":"on"}`)
	delta(p, `{"power":"on"}`, false)
	p.command("r2", "display.screen", "wake", `{}`)
	if r := p.result(); r.GetCode() != "" {
		t.Fatalf("result = %v, want ok", r)
	}

	// unplugged while no link is up: the next link's first delta says so
	_ = p.c.Close(websocket.StatusGoingAway, "restart")
	avail.Set(false, "display.screen")
	p = ls.accept(t)
	p.handshake(false)
	delta(p, `{"power":"on"}`, true)
	if !strings.Contains(buf.String(), `"msg":"capability_availability","capability":"display.screen","available":true`) {
		t.Fatalf("log lacks the availability line:\n%s", buf.String())
	}
}

func TestUnavailableWithoutStateSendsEmptyJSON(t *testing.T) {
	pr := newProvider("display.cec")
	avail := capability.NewAvailability()
	avail.Set(false, "display.cec")
	ls, _ := startSync(t, availBoard{fakeBoard{id: "fake", caps: []capability.Provider{pr}}, avail}, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	d := p.recv().GetStateDelta()
	if d.GetCapability() != "display.cec" || d.GetJson() != "" || !d.GetUnavailable() {
		t.Fatalf("delta = %v", d)
	}
}
