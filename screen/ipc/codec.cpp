#include "ipc/codec.h"

#include <pb_decode.h>
#include <pb_encode.h>

#include <cstring>

namespace cadence::screen::ipc {

void DecodedDeleter::operator()(Envelope* envelope) const {
  if (envelope == nullptr) return;
  pb_release(cadence_screen_v1_Envelope_fields, envelope);
  delete envelope;
}

DecodedEnvelope decodeEnvelope(std::span<const std::uint8_t> frame, std::string* error) {
  DecodedEnvelope out(new Envelope{});
  pb_istream_t stream = pb_istream_from_buffer(frame.data(), frame.size());
  if (!pb_decode(&stream, cadence_screen_v1_Envelope_fields, out.get())) {
    if (error != nullptr) *error = "decode";
    // pb_decode frees what it allocated on failure; the deleter's release then finds nothing
    return nullptr;
  }
  return out;
}

OutEnvelope::OutEnvelope(pb_size_t whichBody) : envelope_(std::make_unique<Envelope>()) {
  std::memset(envelope_.get(), 0, sizeof(Envelope));
  envelope_->which_body = whichBody;
}

char* OutEnvelope::keep(std::string_view text) { return strings_.emplace_back(text).data(); }

bool encodeEnvelope(const Envelope& envelope, std::vector<std::uint8_t>& frame) {
  std::size_t size = 0;
  if (!pb_get_encoded_size(&size, cadence_screen_v1_Envelope_fields, &envelope)) return false;
  frame.resize(size);
  pb_ostream_t stream = pb_ostream_from_buffer(frame.data(), frame.size());
  if (!pb_encode(&stream, cadence_screen_v1_Envelope_fields, &envelope)) return false;
  frame.resize(stream.bytes_written);
  return true;
}

const char* bodyName(pb_size_t which) {
  switch (which) {
    case cadence_screen_v1_Envelope_hello_tag: return "hello";
    case cadence_screen_v1_Envelope_ready_tag: return "ready";
    case cadence_screen_v1_Envelope_link_status_tag: return "link_status";
    case cadence_screen_v1_Envelope_claim_state_tag: return "claim_state";
    case cadence_screen_v1_Envelope_enroll_code_tag: return "enroll_code";
    case cadence_screen_v1_Envelope_server_address_tag: return "server_address";
    case cadence_screen_v1_Envelope_screen_status_tag: return "screen_status";
    case cadence_screen_v1_Envelope_capability_config_tag: return "capability_config";
    case cadence_screen_v1_Envelope_capability_state_tag: return "capability_state";
    case cadence_screen_v1_Envelope_log_line_tag: return "log_line";
    case cadence_screen_v1_Envelope_route_tag: return "route";
    case cadence_screen_v1_Envelope_toast_tag: return "toast";
    case cadence_screen_v1_Envelope_voice_state_tag: return "voice_state";
    case cadence_screen_v1_Envelope_display_power_tag: return "display_power";
    case cadence_screen_v1_Envelope_input_event_tag: return "input_event";
    case cadence_screen_v1_Envelope_text_input_tag: return "text_input";
    case cadence_screen_v1_Envelope_media_play_tag: return "media_play";
    case cadence_screen_v1_Envelope_media_command_tag: return "media_command";
    case cadence_screen_v1_Envelope_media_state_tag: return "media_state";
    case cadence_screen_v1_Envelope_command_result_tag: return "command_result";
    case cadence_screen_v1_Envelope_playback_gate_tag: return "playback_gate";
    case cadence_screen_v1_Envelope_set_level_tag: return "set_level";
    case cadence_screen_v1_Envelope_set_eq_tag: return "set_eq";
    case cadence_screen_v1_Envelope_mute_tag: return "mute";
    case cadence_screen_v1_Envelope_mirror_start_tag: return "mirror_start";
    case cadence_screen_v1_Envelope_mirror_stop_tag: return "mirror_stop";
    case cadence_screen_v1_Envelope_airplay_pin_tag: return "airplay_pin";
    case cadence_screen_v1_Envelope_duck_tag: return "duck";
    case cadence_screen_v1_Envelope_screen_context_tag: return "screen_context";
    case cadence_screen_v1_Envelope_assistant_turn_tag: return "assistant_turn";
    case cadence_screen_v1_Envelope_voice_level_tag: return "voice_level";
    case cadence_screen_v1_Envelope_cards_tag: return "cards";
    case cadence_screen_v1_Envelope_rpc_request_tag: return "rpc_request";
    case cadence_screen_v1_Envelope_rpc_response_tag: return "rpc_response";
    case cadence_screen_v1_Envelope_rpc_event_tag: return "rpc_event";
    case cadence_screen_v1_Envelope_rpc_cancel_tag: return "rpc_cancel";
    case cadence_screen_v1_Envelope_media_session_state_tag: return "media_session_state";
    case cadence_screen_v1_Envelope_tasks_summary_tag: return "tasks_summary";
    case cadence_screen_v1_Envelope_mic_choice_tag: return "mic_choice";
    case cadence_screen_v1_Envelope_player_stats_tag: return "player_stats";
    case cadence_screen_v1_Envelope_volume_state_tag: return "volume_state";
    case cadence_screen_v1_Envelope_media_preparing_tag: return "media_preparing";
    case cadence_screen_v1_Envelope_now_playing_tag: return "now_playing";
    case cadence_screen_v1_Envelope_now_playing_command_tag: return "now_playing_command";
    case cadence_screen_v1_Envelope_alarms_summary_tag: return "alarms_summary";
    default: return "unknown";
  }
}

}  // namespace cadence::screen::ipc
