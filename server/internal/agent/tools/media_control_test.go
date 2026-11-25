package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/music"
)

type controlFake struct {
	calls    []string
	err      error
	restarts int
}

func (f *controlFake) SetShuffle(_ string, on bool) {
	f.calls = append(f.calls, "shuffle "+map[bool]string{true: "on", false: "off"}[on])
}
func (f *controlFake) SetRepeat(_ string, r music.Repeat) {
	f.calls = append(f.calls, "repeat "+string(r))
}
func (f *controlFake) Restart(context.Context, string) error { f.restarts++; return nil }

func (f *controlFake) tool() agent.Tool {
	return tools.MediaControl(tools.MediaDeps{
		Command: func(_ context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error) {
			f.calls = append(f.calls, deviceID+" "+capability+" "+name+" "+string(args))
			return nil, f.err
		},
		Queue: f,
	})
}

func TestMediaControlPolicy(t *testing.T) {
	tool := (&controlFake{}).tool()
	if tool.Effects != agent.Write || !tool.TaintExempt || tool.EventTurns || !tool.SilentOnSuccess {
		t.Fatalf("policy %+v", tool)
	}
	if strings.Contains(tool.Input, "volume") || strings.Contains(tool.Input, "level") {
		t.Fatal("media_control moves the volume")
	}
}

func TestMediaControlSendsPlayerCommandsAndSetsModes(t *testing.T) {
	f := &controlFake{}
	tool := f.tool()
	for _, args := range []string{
		`{"action":"pause"}`, `{"action":"next"}`, `{"action":"seek","positionS":75}`,
		`{"action":"shuffle_on"}`, `{"action":"repeat_one"}`,
	} {
		if _, terr := call(t, tool, "d1", args); terr != nil {
			t.Fatalf("%s: %+v", args, terr)
		}
	}
	want := []string{
		"d1 media.player pause {}", "d1 media.player next {}", `d1 media.player seek {"positionMs":75000}`,
		"shuffle on", "repeat one",
	}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %v", f.calls)
	}
	if _, terr := call(t, tool, "d1", `{"action":"seek"}`); terr == nil || terr.Code != "invalid_arguments" {
		t.Fatalf("seek without a position: %+v", terr)
	}
	if _, terr := call(t, tool, "d1", `{"action":"volume_up"}`); terr == nil {
		t.Fatal("an unknown action ran")
	}
}

func TestMediaControlResumeRestartsAnEndedSong(t *testing.T) {
	f := &controlFake{err: &commands.DeviceError{Code: "no_session"}}
	if _, terr := call(t, f.tool(), "d1", `{"action":"resume"}`); terr != nil || f.restarts != 1 {
		t.Fatalf("%+v, %d restarts", terr, f.restarts)
	}
	if _, terr := call(t, f.tool(), "d1", `{"action":"pause"}`); terr == nil || terr.Code != "nothing_playing" {
		t.Fatalf("%+v", terr)
	}
	f.err = &commands.DeviceError{Code: "unsupported"}
	if _, terr := call(t, f.tool(), "d1", `{"action":"next"}`); terr == nil || terr.Code != "unsupported" {
		t.Fatalf("%+v", terr)
	}
}
