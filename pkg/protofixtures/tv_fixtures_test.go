package protofixtures

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/pkg/cards"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// the screens' frames (tv v1): the link's additions, audiod's routes and tap, and the screen plane
func TestTVGoldenFrames(t *testing.T) {
	link := func() proto.Message { return &cadencev2.Envelope{} }
	audiod := func() proto.Message { return &audiodv1.Envelope{} }
	tap := func() proto.Message { return &audiodv1.TapEnvelope{} }
	screen := func() proto.Message { return &screenv1.Envelope{} }
	cases := map[string]func() proto.Message{
		"v2/rpc_request":                       link,
		"v2/rpc_response":                      link,
		"v2/rpc_response_refused":              link,
		"v2/rpc_event":                         link,
		"v2/rpc_event_page":                    link,
		"v2/rpc_response_page":                 link,
		"v2/rpc_cancel":                        link,
		"v2/screen_context":                    link,
		"v2/assistant_turn":                    link,
		"v2/assistant_turn_no_mic":             link,
		"v2/voice_level":                       link,
		"v2/duck":                              link,
		"v2/duck_release":                      link,
		"v2/mic_open":                          link,
		"v2/session_start_screen":              link,
		"v2/cards":                             link,
		"v2/device_info":                       link,
		"v2/welcome_device_info":               link,
		"v2/media_session_state_video":         link,
		"v2/media_session_state_buffering":     link,
		"v2/media_session_state_handoff":       link,
		"v2/media_session_state_match":         link,
		"audiod/v1/external_source":            audiod,
		"audiod/v1/external_source_unregister": audiod,
		"audiod/v1/external_action":            audiod,
		"audiod/v1/session_control_duck":       audiod,
		"audiod/v1/session_control_level":      audiod,
		"audiod/v1/session_control_eq":         audiod,
		"audiod/v1/session_control_mute":       audiod,
		"audiod/v1/route_control":              audiod,
		"audiod/v1/tap_config":                 audiod,
		"audiod/v1/mirror_state":               audiod,
		"audiod/v1/mirror_state_ended":         audiod,
		"audiod/v1/airplay_pin":                audiod,
		"audiod/v1/airplay_config_libairplay":  audiod,
		"audiod/v1/media_state_route_max":      audiod,
		"audiod/v1/tap/pcm":                    tap,
		"audiod/v1/tap/bands":                  tap,
		"audiod/v1/tap/clock":                  tap,
		"audiod/v1/tap/mirror_frame":           tap,
		"audiod/v1/tap/ring_offer":             tap,
		"audiod/v1/configure_delay":            audiod,
		"audiod/v1/output_delay":               audiod,
		"audiod/v1/external_action_seek":       audiod,
		"audiod/v1/media_state_external":       audiod,
		"v2/rpc_response_card_action":          link,
		"v2/rpc_response_card_action_refused":  link,
		"v2/rpc_response_card_action_page":     link,
		"v2/tasks_summary":                     link,
		"v2/media_session_state_server":        link,
		"v2/assistant_turn_speaker_name":       link,
		"audiod/v1/hls_cast_play":              audiod,
		"audiod/v1/hls_cast_scrub":             audiod,
		"audiod/v1/hls_cast_rate":              audiod,
		"audiod/v1/hls_cast_stop":              audiod,
		"audiod/v1/hls_playback_info":          audiod,
		"audiod/v1/hls_playback_info_ended":    audiod,
		"audiod/v1/mirror_state_stopped":       audiod,
		"audiod/v1/tap/mirror_frame_fragment":  tap,
		"audiod/v1/system_volume":              audiod,
		"audiod/v1/ready_system_volume":        audiod,
		"audiod/v1/configure_system_volume":    audiod,
	}
	for _, name := range []string{
		"hello", "ready", "link_status", "claim_state", "enroll_code", "server_address", "screen_status",
		"capability_config", "log_line", "route", "route_back", "toast", "voice_state", "display_power", "input_event",
		"input_event_api", "text_input", "media_play", "media_play_ambient", "media_command", "media_state",
		"command_result", "playback_gate", "set_level", "set_eq", "mute", "mirror_start", "mirror_stop", "airplay_pin",
		"duck", "screen_context", "assistant_turn", "voice_level", "cards", "rpc_request", "rpc_response", "rpc_event",
		"rpc_cancel", "media_play_renditions", "media_state_gated", "media_state_ended", "hello_card_features",
		"media_session_state", "tasks_summary", "mic_choice", "mic_choice_local", "toast_action", "mirror_stop_stopped",
		"link_status_online", "media_state_buffering", "player_stats", "volume_state", "capability_state",
	} {
		cases["screen/v1/"+name] = screen
	}
	for path, msg := range cases {
		t.Run(path, func(t *testing.T) { golden(t, path, msg) })
	}
}

// a screen frame that carries a link message carries it unchanged, so cadenced relays it without a copy of its own
func TestScreenRelaysLinkMessages(t *testing.T) {
	var env screenv1.Envelope
	if err := proto.Unmarshal(mustRead(t, "screen/v1/duck.binpb"), &env); err != nil {
		t.Fatal(err)
	}
	var link cadencev2.Envelope
	if err := proto.Unmarshal(mustRead(t, "v2/duck.binpb"), &link); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(env.GetDuck(), link.GetDuck()) {
		t.Fatalf("duck differs: %v vs %v", env.GetDuck(), link.GetDuck())
	}
}

// the server's session fields and the tasks count reach the screen as the link carries them
func TestScreenRelaysSessionFieldsAndTasks(t *testing.T) {
	for _, c := range []struct{ screen, link string }{
		{"screen/v1/tasks_summary", "v2/tasks_summary"},
	} {
		var env screenv1.Envelope
		if err := proto.Unmarshal(mustRead(t, c.screen+".binpb"), &env); err != nil {
			t.Fatal(err)
		}
		var link cadencev2.Envelope
		if err := proto.Unmarshal(mustRead(t, c.link+".binpb"), &link); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(env.GetTasksSummary(), link.GetTasksSummary()) {
			t.Fatalf("tasks summary differs: %v vs %v", env.GetTasksSummary(), link.GetTasksSummary())
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, path)) //nolint:gosec // fixed fixture path
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// the cards and pages the link frames carry are cards pkg/cards accepts, as the server sends them
func TestLinkFramesCarryValidCards(t *testing.T) {
	for _, path := range []string{
		"v2/assistant_turn", "v2/cards", "v2/rpc_event_page", "v2/rpc_response_page", "v2/rpc_response_card_action",
		"v2/rpc_response_card_action_refused", "v2/rpc_response_card_action_page",
	} {
		raw, err := os.ReadFile(filepath.Join(fixtures, path+".binpb")) //nolint:gosec // fixed fixture path
		if err != nil {
			t.Fatal(err)
		}
		env := &cadencev2.Envelope{}
		if err := proto.Unmarshal(raw, env); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var found []*cardsv1.Card
		var pages []*cardsv1.Page
		found = append(found, env.GetAssistantTurn().GetCards()...)
		found = append(found, env.GetCards().GetCards()...)
		if p := env.GetRpcEvent().GetPage(); p != nil {
			pages = append(pages, p)
		}
		if p := env.GetRpcResponse().GetPage(); p != nil {
			pages = append(pages, p)
		}
		result := env.GetRpcResponse().GetCardAction()
		if result != nil {
			if err := cards.ValidateActionResult(result); err != nil {
				t.Errorf("%s: %v", path, err)
			}
		}
		if len(found)+len(pages) == 0 && result == nil {
			t.Fatalf("%s carries no card", path)
		}
		for _, c := range found {
			if err := cards.ValidateCard(c); err != nil {
				t.Errorf("%s: %v", path, err)
			}
		}
		for _, p := range pages {
			if err := cards.ValidatePage(p); err != nil {
				t.Errorf("%s: %v", path, err)
			}
		}
	}
}
