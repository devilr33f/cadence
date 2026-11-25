#include "audio/opus-codec.h"
#include "file/wav.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <cmath>
#include <cstdint>
#include <cstring>
#include <numbers>
#include <string>
#include <string_view>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

// the custom wake word's microphone (WakeListen, MicPcm) on raw capture: the file backend, held open while listening
namespace {

constexpr std::size_t kFrame = kEncoderFrameSamples;

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  REQUIRE(text.size() < N);
  std::memcpy(out, text.data(), text.size());
  out[text.size()] = '\0';
}

std::string writeWav(const AudiodHarness& audiod, std::size_t samples) {
  const auto path = audiod.file("in.wav");
  auto writer = WavWriter::create(path, {16000, 1});
  REQUIRE(writer.ok());
  std::vector<std::int16_t> pcm(samples);
  for (std::size_t i = 0; i < samples; ++i)
    pcm[i] = static_cast<std::int16_t>(6000 * std::sin(2 * std::numbers::pi * 300.0 * static_cast<double>(i) / 16000));
  REQUIRE_FALSE(writer.value().write(pcm));
  REQUIRE_FALSE(writer.value().close());
  return path;
}

UniqueFd configured(const AudiodHarness& audiod, std::size_t samples) {
  auto client = connectTo(audiod.socket());
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_configure_tag;
  envelope.body.configure.has_capture = true;
  envelope.body.configure.capture.backend = cadence_audiod_v1_Backend_BACKEND_FILE;
  copyText(envelope.body.configure.capture.file_path, writeWav(audiod, samples));
  sendFrame(client.get(), envelope);
  expectReady(client.get());
  return client;
}

void listen(int fd, bool enabled) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_wake_listen_tag;
  envelope.body.wake_listen.enabled = enabled;
  sendFrame(fd, envelope);
}

void mute(int fd, bool muted) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_mute_state_tag;
  envelope.body.mute_state.muted = muted;
  sendFrame(fd, envelope);
}

cadence_audiod_v1_MicPcm expectPcm(int fd) {
  return expectFrame(fd, cadence_audiod_v1_Envelope_mic_pcm_tag).body.mic_pcm;
}

}  // namespace

TEST_CASE("listen: frames of 20 ms go out while listening, numbered, the first a gap", "[listen]") {
  AudiodHarness audiod;
  auto client = configured(audiod, kFrame * 100);
  CHECK(receive(client.get(), 200ms).kind == Received::Kind::timeout);
  listen(client.get(), true);
  const auto first = expectPcm(client.get());
  CHECK(first.gap);
  CHECK(first.pcm.size == kFrame * 2);
  CHECK(first.at_ms > 0);
  auto previous = first;
  for (int i = 0; i < 5; ++i) {
    const auto next = expectPcm(client.get());
    CHECK(next.seq == previous.seq + 1);
    CHECK_FALSE(next.gap);
    CHECK(next.at_ms >= previous.at_ms);
    previous = next;
  }
  CHECK(audiod.logs().eventually("\"msg\":\"capture_listen_started\""));

  listen(client.get(), false);
  CHECK(audiod.logs().eventually("\"msg\":\"capture_listen_stopped\""));
  while (receive(client.get(), 200ms).kind == Received::Kind::frame) {
  }
}

TEST_CASE("listen: a session streams from the listening capture with its pre-roll, then listening resumes", "[listen]") {
  AudiodHarness audiod;
  auto client = configured(audiod, kFrame * 150);
  listen(client.get(), true);
  std::int64_t hit = 0;
  for (int i = 0; i < 10; ++i)
    hit = expectPcm(client.get()).at_ms;

  Envelope start = cadence_audiod_v1_Envelope_init_zero;
  start.which_body = cadence_audiod_v1_Envelope_capture_start_tag;
  copyText(start.body.capture_start.session_id, "wake-1");
  start.body.capture_start.preroll_from_ms = hit;
  sendFrame(client.get(), start);
  // pcm sent before the start still drains; then the session's opus, from seq 0
  auto received = receive(client.get());
  while (received.kind == Received::Kind::frame && received.envelope.which_body == cadence_audiod_v1_Envelope_mic_pcm_tag)
    received = receive(client.get());
  REQUIRE(received.kind == Received::Kind::frame);
  REQUIRE(received.envelope.which_body == cadence_audiod_v1_Envelope_mic_opus_tag);
  CHECK(std::string_view(received.envelope.body.mic_opus.session_id) == "wake-1");
  CHECK(received.envelope.body.mic_opus.seq == 0);
  // no pcm while the session streams
  for (int i = 0; i < 5; ++i)
    CHECK(expectFrame(client.get(), cadence_audiod_v1_Envelope_mic_opus_tag).body.mic_opus.seq == static_cast<std::uint32_t>(i + 1));

  Envelope stop = cadence_audiod_v1_Envelope_init_zero;
  stop.which_body = cadence_audiod_v1_Envelope_capture_stop_tag;
  copyText(stop.body.capture_stop.session_id, "wake-1");
  sendFrame(client.get(), stop);
  received = receive(client.get());
  while (received.kind == Received::Kind::frame && received.envelope.which_body == cadence_audiod_v1_Envelope_mic_opus_tag)
    received = receive(client.get());
  REQUIRE(received.kind == Received::Kind::frame);
  REQUIRE(received.envelope.which_body == cadence_audiod_v1_Envelope_mic_pcm_tag);
  // after a session the detector starts again
  CHECK(received.envelope.body.mic_pcm.gap);
}

TEST_CASE("listen: nothing goes out while muted", "[listen]") {
  AudiodHarness audiod;
  auto client = configured(audiod, kFrame * 150);
  mute(client.get(), true);
  listen(client.get(), true);
  CHECK(receive(client.get(), 300ms).kind == Received::Kind::timeout);
  mute(client.get(), false);
  CHECK(expectPcm(client.get()).gap);
}
