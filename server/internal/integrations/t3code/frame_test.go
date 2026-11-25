package t3code_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
)

// The golden frames are what effect 4.1.3's RpcMessage encodes with
// RpcSerialization.json (see doc.go): the client's frames byte for byte, and
// the server's as the client must read them.
func TestClientFramesGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/client_frames.golden")
	if err != nil {
		t.Fatal(err)
	}
	payload := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	frames := []t3code.Frame{
		t3code.RequestFrame(1, t3code.MethodSubscribeShell, payload(map[string]any{})),
		t3code.RequestFrame(2, t3code.MethodSubscribeThread, payload(map[string]any{"threadId": "thread-1", "afterSequence": 41})),
		t3code.AckFrame(2),
		t3code.InterruptFrame(2),
		{Tag: "Ping"},
		{Tag: "Eof"},
	}
	var got strings.Builder
	for _, f := range frames {
		b, err := t3code.EncodeFrame(f)
		if err != nil {
			t.Fatal(err)
		}
		got.Write(b)
		got.WriteByte('\n')
	}
	if got.String() != string(want) {
		t.Fatalf("frames:\n%s\nwant:\n%s", got.String(), want)
	}
}

func TestServerFramesGolden(t *testing.T) {
	b, err := os.ReadFile("testdata/server_frames.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string          `json:"name"`
		Frame json.RawMessage `json:"frame"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	byName := map[string]t3code.Frame{}
	for _, c := range cases {
		fs, err := t3code.DecodeFrames(c.Frame)
		if err != nil || len(fs) != 1 {
			t.Fatalf("%s: %v %d", c.Name, err, len(fs))
		}
		byName[c.Name] = fs[0]
	}
	if f := byName["chunk"]; f.Tag != "Chunk" || string(f.RequestID) != "2" || len(f.Values) != 2 {
		t.Errorf("chunk %+v", f)
	}
	if f := byName["stream_end"]; f.Exit == nil || f.Exit.Tag != "Success" || len(f.Exit.Value) != 0 {
		t.Errorf("stream end %+v", f.Exit)
	}
	if f := byName["unary_success"]; f.Exit == nil || string(f.Exit.Value) != `{"sequence":7}` {
		t.Errorf("unary %+v", f.Exit)
	}
	for name, want := range map[string]string{
		"typed_failure": "refused:OrchestrationV2DispatchCommandError", "authorization_failure": "forbidden:EnvironmentAuthorizationError",
		"die": "bad_response:", "interrupt": "unavailable:",
	} {
		f := byName[name]
		if f.Exit == nil || f.Exit.Tag != "Failure" {
			t.Fatalf("%s: %+v", name, f)
		}
		err := t3code.ExitError(f.Exit)
		var e *t3code.Error
		if !errors.As(err, &e) || e.Code+":"+e.Tag != want {
			t.Errorf("%s: %v, want %s", name, err, want)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: upstream text in %q", name, err)
		}
	}
	if f := byName["protocol_error"]; f.Tag != "ClientProtocolError" || len(f.RequestIDs) != 2 {
		t.Errorf("protocol error %+v", f)
	}
	if byName["pong"].Tag != "Pong" || byName["defect"].Tag != "Defect" || byName["unknown_tag"].Tag != "FutureFrame" {
		t.Error("tags")
	}
}

func TestDecodeFramesBatchAndBad(t *testing.T) {
	fs, err := t3code.DecodeFrames([]byte(` [{"_tag":"Pong"},{"_tag":"Chunk","requestId":"9","values":[1],"extra":{}}] `))
	if err != nil || len(fs) != 2 || fs[1].Tag != "Chunk" {
		t.Fatalf("%v %+v", err, fs)
	}
	for _, bad := range []string{``, `{}`, `[{"_tag":"Pong"},{}]`, `nope`, `{"_tag":1}`} {
		if _, err := t3code.DecodeFrames([]byte(bad)); err == nil {
			t.Errorf("%q decoded", bad)
		}
	}
}
