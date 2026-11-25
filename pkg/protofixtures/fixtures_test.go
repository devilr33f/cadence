package protofixtures

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

var update = flag.Bool("update", false, "rewrite .binpb files from .txtpb")

const fixtures = "../../proto/fixtures"

func TestGoldenFrames(t *testing.T) {
	cases := []struct {
		name string
		path string
		msg  func() proto.Message
	}{
		{"hello", "v2/hello", func() proto.Message { return &cadencev2.Envelope{} }},
		{"ops_start", "v2/ops_start", func() proto.Message { return &cadencev2.Envelope{} }},
		{"ops_progress", "v2/ops_progress", func() proto.Message { return &cadencev2.Envelope{} }},
		{"ops_result", "v2/ops_result", func() proto.Message { return &cadencev2.Envelope{} }},
		{"capability_report", "v2/capability_report", func() proto.Message { return &cadencev2.Envelope{} }},
		{"capability_report_part", "v2/capability_report_part", func() proto.Message { return &cadencev2.Envelope{} }},
		{"config_set", "v2/config_set", func() proto.Message { return &cadencev2.Envelope{} }},
		{"config_applied", "v2/config_applied", func() proto.Message { return &cadencev2.Envelope{} }},
		{"state_delta", "v2/state_delta", func() proto.Message { return &cadencev2.Envelope{} }},
		{"session_start", "v2/session_start", func() proto.Message { return &cadencev2.Envelope{} }},
		{"session_start_after_ring", "v2/session_start_after_ring", func() proto.Message { return &cadencev2.Envelope{} }},
		{"session_start_event", "v2/session_start_event", func() proto.Message { return &cadencev2.Envelope{} }},
		{"mic_audio", "v2/mic_audio", func() proto.Message { return &cadencev2.Envelope{} }},
		{"session_cancel", "v2/session_cancel", func() proto.Message { return &cadencev2.Envelope{} }},
		{"session_event", "v2/session_event", func() proto.Message { return &cadencev2.Envelope{} }},
		{"session_close", "v2/session_close", func() proto.Message { return &cadencev2.Envelope{} }},
		{"stream_start", "v2/stream_start", func() proto.Message { return &cadencev2.Envelope{} }},
		{"audio_down", "v2/audio_down", func() proto.Message { return &cadencev2.Envelope{} }},
		{"stream_end", "v2/stream_end", func() proto.Message { return &cadencev2.Envelope{} }},
		{"ping", "audiod/v1/ping", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_pong", "audiod/v1/pong", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_configure", "audiod/v1/configure", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_ready", "audiod/v1/ready", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_capture_start", "audiod/v1/capture_start", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_capture_stop", "audiod/v1/capture_stop", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_mic_opus", "audiod/v1/mic_opus", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_capture_failed", "audiod/v1/capture_failed", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_stream_start", "audiod/v1/stream_start", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_audio_down", "audiod/v1/audio_down", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_stream_end", "audiod/v1/stream_end", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_stream_cancel", "audiod/v1/stream_cancel", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_stream_done", "audiod/v1/stream_done", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_volume_set", "audiod/v1/volume_set", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_volume_applied", "audiod/v1/volume_applied", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_tone", "audiod/v1/tone", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_tone_start_alarm", "audiod/v1/tone_start_alarm", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_tone_start_timer_max", "audiod/v1/tone_start_timer_max", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_tone_stop", "audiod/v1/tone_stop", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_tone_state", "audiod/v1/tone_state", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_tone_state_gated", "audiod/v1/tone_state_gated", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_volume_set_transient", "audiod/v1/volume_set_transient", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_volume_applied_ramp_refused", "audiod/v1/volume_applied_ramp_refused", func() proto.Message { return &audiodv1.Envelope{} }},
		{"session_close_follow_up", "v2/session_close_follow_up", func() proto.Message { return &cadencev2.Envelope{} }},
		{"audiod_log_line", "audiod/v1/log_line", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_log_line_max", "audiod/v1/log_line_max", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_log_line_continued", "audiod/v1/log_line_continued", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_url_play", "audiod/v1/url_play", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_url_play_max", "audiod/v1/url_play_max", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_media_command", "audiod/v1/media_command", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_command_result", "audiod/v1/command_result", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_media_state", "audiod/v1/media_state", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_media_state_idle", "audiod/v1/media_state_idle", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_media_state_max", "audiod/v1/media_state_max", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_duck_set", "audiod/v1/duck_set", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_media_settings", "audiod/v1/media_settings", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_viz_frame", "audiod/v1/viz_frame", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_configure_media", "audiod/v1/configure_media", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_soloist_config", "audiod/v1/soloist_config", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_soloist_status", "audiod/v1/soloist_status", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_soloist_probe", "audiod/v1/soloist_probe", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_soloist_probe_result", "audiod/v1/soloist_probe_result", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_soloist_switch", "audiod/v1/soloist_switch", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_sender_volume", "audiod/v1/sender_volume", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_sender_volume_set", "audiod/v1/sender_volume_set", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_airplay_config", "audiod/v1/airplay_config", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_airplay_status", "audiod/v1/airplay_status", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_airplay_status_failed", "audiod/v1/airplay_status_failed", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_configure_pickle", "audiod/v1/configure_pickle", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_configure_pickle_max", "audiod/v1/configure_pickle_max", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_wake_event", "audiod/v1/wake_event", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_configure_vk", "audiod/v1/configure_vk", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_wake_event_vk", "audiod/v1/wake_event_vk", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_command_word", "audiod/v1/command_word", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_mute_state", "audiod/v1/mute_state", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_vendor_status", "audiod/v1/vendor_status", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_vendor_status_safe", "audiod/v1/vendor_status_safe", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_logs_set", "audiod/v1/logs_set", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_logs_set_off", "audiod/v1/logs_set_off", func() proto.Message { return &audiodv1.Envelope{} }},
		{"capability_command", "v2/capability_command", func() proto.Message { return &cadencev2.Envelope{} }},
		{"capability_command_grant", "v2/capability_command_grant", func() proto.Message { return &cadencev2.Envelope{} }},
		{"command_result", "v2/command_result", func() proto.Message { return &cadencev2.Envelope{} }},
		{"media_session_state", "v2/media_session_state", func() proto.Message { return &cadencev2.Envelope{} }},
		{"capability_report_commands", "v2/capability_report_commands", func() proto.Message { return &cadencev2.Envelope{} }},
		{"session_start_traced", "v2/session_start_traced", func() proto.Message { return &cadencev2.Envelope{} }},
		{"stream_start_traced", "v2/stream_start_traced", func() proto.Message { return &cadencev2.Envelope{} }},
		{"rpc_request_traced", "v2/rpc_request_traced", func() proto.Message { return &cadencev2.Envelope{} }},
		{"rpc_response_traced", "v2/rpc_response_traced", func() proto.Message { return &cadencev2.Envelope{} }},
		{"rpc_event_traced", "v2/rpc_event_traced", func() proto.Message { return &cadencev2.Envelope{} }},
		{"audiod_capture_start_traced", "audiod/v1/capture_start_traced", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_stream_start_traced", "audiod/v1/stream_start_traced", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_wake_event_ns", "audiod/v1/wake_event_ns", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_trace_mark", "audiod/v1/trace_mark", func() proto.Message { return &audiodv1.Envelope{} }},
		{"audiod_trace_mark_interval", "audiod/v1/trace_mark_interval", func() proto.Message { return &audiodv1.Envelope{} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			decoded := golden(t, c.path, c.msg)
			if h, ok := decoded.(*cadencev2.Envelope); ok && c.name == "hello" {
				if h.GetHello().GetBoard() != "sim" || h.GetHello().GetProtocol() != 2 {
					t.Fatalf("unexpected hello: %v", h.GetHello())
				}
			}
			if e, ok := decoded.(*audiodv1.Envelope); ok {
				checkVK(t, c.name, e)
			}
		})
	}
}

// checkVK holds the vk capsula mini's keyword frames to the board's values: the stock model's path and sha256, the
// tightened thresholds and the engine's token
func checkVK(t *testing.T, name string, e *audiodv1.Envelope) {
	t.Helper()
	switch name {
	case "audiod_configure_vk":
		k := e.GetConfigure().GetCapture().GetKeyword()
		if k.GetKind() != "vk-kws" || k.GetModelPath() != "/usr/share/voice-agent/model.bin" ||
			k.GetModelSha256() != "8b33272b9fa8569f85630fd6208f475021ea806a7d79168e070a374569732f3b" ||
			k.GetPosThreshold() != 0.999 || k.GetSpThreshold() != 0.2 || k.GetRearmFrames() != 147 || k.GetConsecutiveCalls() != 1 {
			t.Fatalf("unexpected keyword: %v", k)
		}
		if e.GetConfigure().GetCapture().GetEngines() != nil {
			t.Fatal("the vk board runs no vendor engines")
		}
	case "audiod_wake_event_vk":
		if w := e.GetWakeEvent(); w.GetEngine() != "vk-kws" || w.GetScore() != 0.9995 || w.GetAtMs() != 1763500000000 {
			t.Fatalf("unexpected wake: %v", w)
		}
	}
}

// golden parses path's .txtpb, rewrites its .binpb under -update, checks the two agree and returns the decoded frame
func golden(t *testing.T, path string, msg func() proto.Message) proto.Message {
	t.Helper()
	txt, err := os.ReadFile(filepath.Join(fixtures, path+".txtpb"))
	if err != nil {
		t.Fatal(err)
	}
	parsed := msg()
	if err := prototext.Unmarshal(txt, parsed); err != nil {
		t.Fatal(err)
	}
	want, err := proto.MarshalOptions{Deterministic: true}.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(fixtures, path+".binpb")
	if *update {
		if err := os.WriteFile(binPath, want, 0o644); err != nil { //nolint:gosec // public fixture
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(binPath) //nolint:gosec // fixed fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: binpb differs from txtpb (run with -update)", path)
	}
	decoded := msg()
	if err := proto.Unmarshal(got, decoded); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(decoded, parsed) {
		t.Fatalf("%s: decoded frame differs from parsed text", path)
	}
	return decoded
}

func TestEarlierFixturesStillDecode(t *testing.T) {
	read := func(path string, msg proto.Message) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(fixtures, path)) //nolint:gosec // fixed fixture path
		if err != nil {
			t.Fatal(err)
		}
		if err := proto.Unmarshal(raw, msg); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	var cfg audiodv1.Envelope
	read("audiod/v1/configure.binpb", &cfg)
	out := cfg.GetConfigure().GetOutput()
	if out.GetMixerControl() != "Master" || out.GetChannels() != 0 || out.GetEq() != nil || cfg.GetConfigure().GetCaFile() != "" {
		t.Fatalf("unexpected configure: %v", cfg.GetConfigure())
	}
	var rep cadencev2.Envelope
	read("v2/capability_report.binpb", &rep)
	d := rep.GetCapabilityReport().GetDescriptors()
	if len(d) != 1 || d[0].GetId() != "audio.output" || len(d[0].GetCommands()) != 0 {
		t.Fatalf("unexpected report: %v", rep.GetCapabilityReport())
	}
}

// the card examples (pkg/cards/testdata, protojson) as golden binaries in proto/fixtures/cards/v1, card_<name> and
// page_<name>, so the screen's c++ renderer decodes the very bytes the server encodes
func TestGoldenCards(t *testing.T) {
	for _, set := range []struct {
		dir, prefix string
		msg         func() proto.Message
	}{
		{"cards", "card_", func() proto.Message { return &cardsv1.Card{} }},
		{"pages", "page_", func() proto.Message { return &cardsv1.Page{} }},
	} {
		paths, err := filepath.Glob(filepath.Join("../cards/testdata", set.dir, "*.json"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("no card examples in %s: %v", set.dir, err)
		}
		for _, p := range paths {
			name := set.prefix + strings.TrimSuffix(filepath.Base(p), ".json")
			t.Run(name, func(t *testing.T) {
				raw, err := os.ReadFile(p) //nolint:gosec // fixed testdata path
				if err != nil {
					t.Fatal(err)
				}
				parsed := set.msg()
				if err := protojson.Unmarshal(raw, parsed); err != nil {
					t.Fatal(err)
				}
				want, err := proto.MarshalOptions{Deterministic: true}.Marshal(parsed)
				if err != nil {
					t.Fatal(err)
				}
				binPath := filepath.Join(fixtures, "cards/v1", name+".binpb")
				if *update {
					if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil { //nolint:gosec // public fixtures
						t.Fatal(err)
					}
					if err := os.WriteFile(binPath, want, 0o644); err != nil { //nolint:gosec // public fixture
						t.Fatal(err)
					}
				}
				got, err := os.ReadFile(binPath) //nolint:gosec // fixed fixture path
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("%s: binpb differs from its example (run with -update)", name)
				}
			})
		}
	}
}
