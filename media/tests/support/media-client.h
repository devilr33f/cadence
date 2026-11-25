#pragma once

#include "support/audiod-client.h"
#include "support/playback-client.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cstdint>
#include <string_view>
#include <vector>

// cadenced's side of audiod's media messages, for the suites that drive media sessions and urls over its socket
namespace cadence::test_support {

using MediaStateFrame = cadence_audiod_v1_MediaState;
using CommandResultFrame = cadence_audiod_v1_CommandResult;

inline constexpr auto kSessionMode = cadence_audiod_v1_UrlMode_URL_MODE_SESSION;
inline constexpr auto kOverlayMode = cadence_audiod_v1_UrlMode_URL_MODE_OVERLAY;
inline constexpr auto kPlaying = cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_PLAYING;
inline constexpr auto kPaused = cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_PAUSED;
inline constexpr auto kIdle = cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_IDLE;
inline constexpr auto kUrlKind = cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_URL;
inline constexpr auto kNoAction = cadence_audiod_v1_MediaAction_MEDIA_ACTION_UNSPECIFIED;
inline constexpr auto kPause = cadence_audiod_v1_MediaAction_MEDIA_ACTION_PAUSE;
inline constexpr auto kResume = cadence_audiod_v1_MediaAction_MEDIA_ACTION_RESUME;
inline constexpr auto kStop = cadence_audiod_v1_MediaAction_MEDIA_ACTION_STOP;
inline constexpr auto kSeek = cadence_audiod_v1_MediaAction_MEDIA_ACTION_SEEK;
inline constexpr auto kNext = cadence_audiod_v1_MediaAction_MEDIA_ACTION_NEXT;
inline constexpr auto kPrevious = cadence_audiod_v1_MediaAction_MEDIA_ACTION_PREVIOUS;

inline audiod::Envelope urlPlay(std::string_view requestId, std::string_view url,
                                cadence_audiod_v1_UrlMode mode = kSessionMode) {
  audiod::Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_url_play_tag;
  auto& body = envelope.body.url_play;
  copyText(body.request_id, requestId);
  copyText(body.url, url);
  body.mode = mode;
  return envelope;
}

inline audiod::Envelope mediaCommand(std::string_view requestId, cadence_audiod_v1_MediaAction action,
                                     std::uint64_t positionMs = 0) {
  audiod::Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_media_command_tag;
  auto& body = envelope.body.media_command;
  copyText(body.request_id, requestId);
  body.action = action;
  body.position_ms = positionMs;
  return envelope;
}

// what came in until the pong to a fresh ping
struct Frames {
  std::vector<CommandResultFrame> results;
  std::vector<MediaStateFrame> states;
  std::vector<audiod::Envelope> others;
};

inline void collect(Frames& frames, const audiod::Envelope& envelope) {
  if (envelope.which_body == cadence_audiod_v1_Envelope_command_result_tag)
    frames.results.push_back(envelope.body.command_result);
  else if (envelope.which_body == cadence_audiod_v1_Envelope_media_state_tag)
    frames.states.push_back(envelope.body.media_state);
  else
    frames.others.push_back(envelope);
}

inline Frames untilPong(int fd) {
  static std::uint64_t seq = 1000;
  sendFrame(fd, ping(++seq));
  Frames frames;
  for (;;) {
    const auto received = receive(fd);
    REQUIRE(received.kind == Received::Kind::frame);
    if (received.envelope.which_body == cadence_audiod_v1_Envelope_pong_tag && received.envelope.body.pong.seq == seq)
      return frames;
    collect(frames, received.envelope);
  }
}

// the one result among frames for that id, with that code
inline void expectResult(const Frames& frames, std::string_view requestId, std::string_view code) {
  const auto matching = [&](const CommandResultFrame& result) {
    return std::string_view(result.request_id) == requestId;
  };
  REQUIRE(std::count_if(frames.results.begin(), frames.results.end(), matching) == 1);
  const auto result = *std::find_if(frames.results.begin(), frames.results.end(), matching);
  CHECK(std::string_view(result.code) == code);
  CHECK(result.ok == code.empty());
}

// sends one command and checks it got exactly one answer, with that id and code, and returns what else came
inline Frames answered(int fd, const audiod::Envelope& command, std::string_view requestId, std::string_view code) {
  sendFrame(fd, command);
  auto frames = untilPong(fd);
  REQUIRE(frames.results.size() == 1);
  expectResult(frames, requestId, code);
  return frames;
}

inline MediaStateFrame onlyState(const Frames& frames) {
  REQUIRE(frames.states.size() == 1);
  return frames.states[0];
}

inline void expectIdle(const MediaStateFrame& state) {
  CHECK(std::string_view(state.session_id).empty());
  CHECK(state.state == kIdle);
  CHECK(state.rate == 0.0f);
  CHECK(state.position_ms == 0);
  CHECK(state.duration_ms == 0);
  CHECK_FALSE(state.has_metadata);
  CHECK(state.actions_count == 0);
  CHECK_FALSE(state.has_volume);
}

}  // namespace cadence::test_support
