package mcpserver_test

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestToolsetListed is the whole toolset over the wire: the service-backed
// tools and the agent's own, with the turn's-own ones left out and the
// device note added where a tool takes a device.
func TestToolsetListed(t *testing.T) {
	r := newRig(t, nil)
	cs := r.client("admin")
	names := r.tools(cs)
	// the sdk serves a client's tools sorted by name
	want := []string{"assistant_turn", "fake_read", "fake_write", "get_device", "get_media_link", "get_playback",
		"list_devices", "list_favorites", "play_url", "playback_history", "say", "send_command"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools: %v", names)
	}
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		switch tool.Name {
		case "fake_write":
			if !strings.Contains(tool.Description, "device argument is required") {
				t.Fatalf("fake_write description: %s", tool.Description)
			}
		case "fake_read":
			if strings.Contains(tool.Description, "device argument is required") {
				t.Fatalf("fake_read description: %s", tool.Description)
			}
		}
	}
}

// TestToolListFollowsTokenPolicy is a token's named tools: the list it sees
// and the calls it may make agree.
func TestToolListFollowsTokenPolicy(t *testing.T) {
	r := newRig(t, nil)
	cs := r.client("mcp-two")
	if names := r.tools(cs); strings.Join(names, ",") != "list_devices,say" {
		t.Fatalf("narrowed list: %v", names)
	}
	if _, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "get_playback"}); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("a tool outside the policy answered: %v", err)
	}
	if _, isErr := r.call(cs, "say", map[string]any{"device": "kitchen", "text": "hi"}); isErr {
		t.Fatal("a tool inside the policy was refused")
	}
	if names := r.tools(r.client("mcp")); len(names) < 12 {
		t.Fatalf("an empty policy should see every tool: %v", names)
	}
}

// TestAgentToolCall runs an agent tool through the wire: its device argument
// resolved, its call a pressed one, its answer the tool's own JSON.
func TestAgentToolCall(t *testing.T) {
	r := newRig(t, nil)
	cs := r.client("mcp")
	answer, isErr := r.call(cs, "fake_write", map[string]any{"device": "kitchen", "value": 3})
	if isErr || answer != `{"done":true}` {
		t.Fatalf("answer %q isErr %v", answer, isErr)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) != 1 || r.calls[0].DeviceID != "dev-1" || r.calls[0].Trigger.String() != "TRIGGER_BUTTON" {
		t.Fatalf("call: %+v", r.calls)
	}
	if !strings.HasPrefix(r.calls[0].TurnID, "mcp_") {
		t.Fatalf("turn id: %s", r.calls[0].TurnID)
	}
}

// TestAgentToolNeedsDevice refuses a device tool's call without its device
// argument, and a device nobody has with an unknown name.
func TestAgentToolNeedsDevice(t *testing.T) {
	r := newRig(t, nil)
	cs := r.client("mcp")
	if answer, isErr := r.call(cs, "fake_write", map[string]any{"value": 1}); !isErr || !strings.Contains(answer, "device_required") {
		t.Fatalf("no device: %q %v", answer, isErr)
	}
	if answer, isErr := r.call(cs, "fake_write", map[string]any{"device": "attic"}); !isErr || !strings.Contains(answer, "unknown_device") {
		t.Fatalf("unknown device: %q %v", answer, isErr)
	}
	// schema validation holds as in a turn
	if answer, isErr := r.call(cs, "fake_write", map[string]any{"device": "kitchen", "value": "no"}); !isErr {
		t.Fatalf("bad args passed: %q %v", answer, isErr)
	}
}

// TestListDevicesAndPlayback reads the household's devices and sessions.
func TestListDevicesAndPlayback(t *testing.T) {
	r := newRig(t, nil)
	cs := r.client("mcp")
	answer, isErr := r.call(cs, "list_devices", nil)
	if isErr {
		t.Fatalf("list_devices: %s", answer)
	}
	var out struct {
		Devices []struct {
			ID, Name string
			Online   bool
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(answer), &out); err != nil || len(out.Devices) != 1 ||
		out.Devices[0].Name != "kitchen" || !out.Devices[0].Online {
		t.Fatalf("devices: %s (%v)", answer, err)
	}
	answer, isErr = r.call(cs, "get_playback", nil)
	if isErr || !strings.Contains(answer, `"devices":[]`) {
		t.Fatalf("playback: %q %v", answer, isErr)
	}
}

// TestSayAndAssistantTurn speak and run the typed assistant: the seams the
// dashboard's own routes use, their codes mapped.
func TestSayAndAssistantTurn(t *testing.T) {
	r := newRig(t, nil)
	cs := r.client("mcp")
	if answer, isErr := r.call(cs, "say", map[string]any{"device": "kitchen", "text": "hi there"}); isErr {
		t.Fatalf("say: %s", answer)
	}
	r.mu.Lock()
	if len(r.said) != 1 || r.said[0].Text != "hi there" {
		t.Fatalf("said: %+v", r.said)
	}
	r.mu.Unlock()

	answer, isErr := r.call(cs, "assistant_turn", map[string]any{"device": "kitchen", "text": "hello"})
	if isErr || !strings.Contains(answer, `"reply":"the reply"`) {
		t.Fatalf("turn: %q %v", answer, isErr)
	}
	if answer, isErr := r.call(cs, "assistant_turn", map[string]any{"device": "kitchen", "text": "busy"}); !isErr || !strings.Contains(answer, "busy") {
		t.Fatalf("busy: %q %v", answer, isErr)
	}
	if answer, isErr := r.call(cs, "assistant_turn", map[string]any{"device": "kitchen", "text": "fail"}); !isErr || !strings.Contains(answer, "llm_failed") {
		t.Fatalf("failed: %q %v", answer, isErr)
	}
}

// TestPlayURL sends the media.url play command the HA integration sends.
func TestPlayURL(t *testing.T) {
	r := newRig(t, nil)
	cs := r.client("mcp")
	if answer, isErr := r.call(cs, "play_url", map[string]any{"device": "kitchen", "url": "https://x.example/a.mp3"}); isErr {
		t.Fatalf("play_url: %s", answer)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sent) != 1 || r.sent[0].DeviceID != "dev-1" || r.sent[0].Capability != "media.url" || r.sent[0].Name != "play" {
		t.Fatalf("sent: %+v", r.sent)
	}
	var args struct {
		URL  string `json:"url"`
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(r.sent[0].Args, &args); err != nil || args.URL != "https://x.example/a.mp3" || args.Mode != "session" {
		t.Fatalf("args: %s (%v)", r.sent[0].Args, err)
	}
}

// TestMediaLink mints the three kinds of link: a music result's native
// stream, a YouTube video's audio and a catalog episode, each with its
// expiry, and refuses what it cannot resolve.
func TestMediaLink(t *testing.T) {
	r := newRig(t, nil)
	cs := r.client("mcp")
	answer, isErr := r.call(cs, "get_media_link", map[string]any{"device": "kitchen", "musicId": "r1"})
	if isErr || !strings.Contains(answer, `"url":"https://pub.example/v2/stream/tok-dev-1"`) ||
		!strings.Contains(answer, `"expiresAt":"2027-01-01T00:00:00Z"`) {
		t.Fatalf("music link: %q %v", answer, isErr)
	}
	answer, isErr = r.call(cs, "get_media_link", map[string]any{"videoId": "vid1"})
	if isErr || !strings.Contains(answer, `"kind":"youtube"`) {
		t.Fatalf("yt link: %q %v", answer, isErr)
	}
	answer, isErr = r.call(cs, "get_media_link", map[string]any{"ref": "cat-9", "episode": "5"})
	if isErr || !strings.Contains(answer, `"url":"https://cat.example/play/1"`) || !strings.Contains(answer, `"durationS":90`) {
		t.Fatalf("catalog link: %q %v", answer, isErr)
	}
	if answer, isErr := r.call(cs, "get_media_link", map[string]any{"device": "kitchen", "musicId": "r9"}); !isErr || !strings.Contains(answer, "unknown_result") {
		t.Fatalf("unknown result: %q %v", answer, isErr)
	}
	if answer, isErr := r.call(cs, "get_media_link", map[string]any{}); !isErr || !strings.Contains(answer, "bad_arguments") {
		t.Fatalf("no argument: %q %v", answer, isErr)
	}
}

// TestEndpointScopes holds the auth at the route: a bearer that is none, one
// without the mcp scope, and one with it.
func TestEndpointScopes(t *testing.T) {
	r := newRig(t, nil)
	if code, _ := r.post("", `{}`); code != 401 {
		t.Fatalf("no bearer: %d", code)
	}
	if code, _ := r.post("ha", `{}`); code != 403 {
		t.Fatalf("ha bearer: %d", code)
	}
	if code, _ := r.post("mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`); code == 401 || code == 403 {
		t.Fatalf("mcp bearer: %d", code)
	}
}

// TestLogsCarryCodesOnly is the privacy contract: the package's own logs
// name the tool, the code and the milliseconds, never an argument, a reply,
// a title or a link.
func TestLogsCarryCodesOnly(t *testing.T) {
	r := newRig(t, nil)
	cs := r.client("mcp")
	if _, isErr := r.call(cs, "say", map[string]any{"device": "kitchen", "text": "secret words"}); isErr {
		t.Fatal("say failed")
	}
	if _, isErr := r.call(cs, "get_media_link", map[string]any{"device": "kitchen", "musicId": "r1"}); isErr {
		t.Fatal("link failed")
	}
	logs := r.logs.String()
	for _, secret := range []string{"secret words", "stream/tok-", "the reply", "kitchen"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("log carries %q: %s", secret, logs)
		}
	}
	if !strings.Contains(logs, "tool=say") || !strings.Contains(logs, "code=ok") {
		t.Fatalf("no tool lines: %s", logs)
	}
}
