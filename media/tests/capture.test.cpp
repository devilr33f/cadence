#include "audio/opus-codec.h"
#include "audiod/capture.h"
#include "audiod/output.h"
#include "audiod/sys.h"
#include "file/wav.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <memory>
#include <numbers>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

constexpr auto kFile = cadence_audiod_v1_Backend_BACKEND_FILE;
constexpr std::size_t kFrame = kEncoderFrameSamples;

std::string event(std::string_view msg) { return "\"msg\":\"" + std::string(msg) + "\""; }

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  REQUIRE(text.size() < N);
  std::memcpy(out, text.data(), text.size());
  out[text.size()] = '\0';
}

std::string writeWav(const AudiodHarness& audiod, std::string_view name, std::size_t samples) {
  const auto path = audiod.file(name);
  auto writer = WavWriter::create(path, {16000, 1});
  REQUIRE(writer.ok());
  std::vector<std::int16_t> pcm(samples);
  for (std::size_t index = 0; index < samples; ++index)
    pcm[index] = static_cast<std::int16_t>(
        8000 * std::sin(2 * std::numbers::pi * 440.0 * static_cast<double>(index) / 16000));
  REQUIRE_FALSE(writer.value().write(pcm));
  REQUIRE_FALSE(writer.value().close());
  return path;
}

Envelope configureCapture(const std::string& path) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_configure_tag;
  envelope.body.configure.has_capture = true;
  envelope.body.configure.capture.backend = kFile;
  copyText(envelope.body.configure.capture.file_path, path);
  return envelope;
}

Envelope captureStart(std::string_view id) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_capture_start_tag;
  copyText(envelope.body.capture_start.session_id, id);
  return envelope;
}

Envelope captureStop(std::string_view id) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_capture_stop_tag;
  copyText(envelope.body.capture_stop.session_id, id);
  return envelope;
}

UniqueFd connectConfigured(const AudiodHarness& audiod, const std::string& wav) {
  auto client = connectTo(audiod.socket());
  sendFrame(client.get(), configureCapture(wav));
  expectReady(client.get());
  return client;
}

struct Mic {
  std::string session;
  std::uint32_t seq;
};

Mic expectMic(int fd, std::chrono::milliseconds limit = 2s) {
  const auto received = receive(fd, limit);
  REQUIRE(received.kind == Received::Kind::frame);
  REQUIRE(received.envelope.which_body == cadence_audiod_v1_Envelope_mic_opus_tag);
  const auto& mic = received.envelope.body.mic_opus;
  // every packet is one valid 20 ms packet
  OpusDecoder decoder;
  const auto decoded = decoder.decode(std::span<const std::uint8_t>(mic.opus.bytes, mic.opus.size));
  CHECK(decoded.ok());
  return {mic.session_id, mic.seq};
}

void expectNoFrame(int fd, std::chrono::milliseconds limit = 250ms) {
  CHECK(receive(fd, limit).kind == Received::Kind::timeout);
}

std::string expectFailed(int fd, std::string_view session) {
  const auto envelope = expectFrame(fd, cadence_audiod_v1_Envelope_capture_failed_tag);
  CHECK(std::string_view(envelope.body.capture_failed.session_id) == session);
  return envelope.body.capture_failed.reason;
}

}  // namespace

TEST_CASE("capture: no mic without a session", "[capture]") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, writeWav(audiod, "in.wav", kFrame * 5));
  expectNoFrame(client.get());
  expectPong(client.get(), 1);

  sendFrame(client.get(), captureStart("s1"));
  CHECK(expectMic(client.get()).session == "s1");
}

TEST_CASE("capture: a refused start sends no audio and a CaptureFailed that echoes only a printable id", "[capture]") {
  AudiodHarness audiod;
  auto client = connectTo(audiod.socket());
  sendFrame(client.get(), captureStart("s1"));
  CHECK(expectFailed(client.get(), "s1") == "unconfigured");
  expectNoFrame(client.get());
  CHECK(audiod.logs().count(event("capture_refused") + R"(,"reason":"unconfigured")") == 1);

  sendFrame(client.get(), configureCapture(writeWav(audiod, "in.wav", kFrame * 5)));
  expectReady(client.get());
  sendFrame(client.get(), captureStart("../x"));
  CHECK(expectFailed(client.get(), "../x") == "session_id");
  sendFrame(client.get(), captureStart(std::string(65, 'a')));
  CHECK(expectFailed(client.get(), std::string(65, 'a')) == "session_id");

  // cadenced could not read these back as it sent them (an empty id, a control byte, no utf-8): logged only
  sendFrame(client.get(), captureStart(""));
  sendFrame(client.get(), captureStart("a\x01" "b"));
  sendFrame(client.get(), captureStart("\xff"));
  expectNoFrame(client.get());
  CHECK(audiod.logs().count(event("capture_refused") + R"(,"reason":"session_id")") == 5);
}

TEST_CASE("capture: frames carry the session id and a seq from 0", "[capture]") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, writeWav(audiod, "in.wav", kFrame * 10));
  sendFrame(client.get(), captureStart("turn-1"));
  for (std::uint32_t seq = 0; seq < 10; ++seq) {
    const auto mic = expectMic(client.get());
    CHECK(mic.session == "turn-1");
    CHECK(mic.seq == seq);
  }
  expectNoFrame(client.get());
}

TEST_CASE("capture: a new start supersedes the running session", "[capture]") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, writeWav(audiod, "in.wav", kFrame * 100));
  sendFrame(client.get(), captureStart("a"));
  CHECK(expectMic(client.get()).seq == 0);
  sendFrame(client.get(), captureStart("b"));

  // frames of a may still be in the socket; once b shows, only b follows, from seq 0
  Mic mic = expectMic(client.get());
  while (mic.session == "a")
    mic = expectMic(client.get());
  CHECK(mic.session == "b");
  CHECK(mic.seq == 0);
  for (std::uint32_t seq = 1; seq < 4; ++seq) {
    mic = expectMic(client.get());
    CHECK(mic.session == "b");
    CHECK(mic.seq == seq);
  }
}

TEST_CASE("capture: stop ends the session, a stale stop does not", "[capture]") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, writeWav(audiod, "in.wav", kFrame * 100));
  sendFrame(client.get(), captureStart("a"));
  expectMic(client.get());
  sendFrame(client.get(), captureStop("other"));
  expectMic(client.get());

  sendFrame(client.get(), captureStop("a"));
  CHECK(audiod.logs().eventually(event("capture_stopped")));
  // what was queued before the stop drains, then nothing
  while (receive(client.get(), 250ms).kind == Received::Kind::frame) {
  }
  expectPong(client.get(), 2);
}

TEST_CASE("capture: a file that ends leaves the session quiet and the daemon healthy", "[capture]") {
  AudiodHarness audiod;
  // five whole frames and a short tail, which goes out zero padded
  auto client = connectConfigured(audiod, writeWav(audiod, "in.wav", kFrame * 5 + 100));
  sendFrame(client.get(), captureStart("a"));
  for (std::uint32_t seq = 0; seq < 6; ++seq)
    CHECK(expectMic(client.get()).seq == seq);
  expectNoFrame(client.get());
  expectPong(client.get(), 1);

  sendFrame(client.get(), captureStop("a"));
  CHECK(audiod.logs().eventually(event("capture_stopped")));
  sendFrame(client.get(), captureStart("b"));
  const auto again = expectMic(client.get());
  CHECK(again.session == "b");
  CHECK(again.seq == 0);
}

TEST_CASE("capture: cadenced going away ends the session, a new client starts clean", "[capture]") {
  AudiodHarness audiod;
  const auto wav = writeWav(audiod, "in.wav", kFrame * 200);
  {
    auto client = connectConfigured(audiod, wav);
    sendFrame(client.get(), captureStart("a"));
    expectMic(client.get());
  }
  REQUIRE(audiod.logs().eventually(event("capture_stopped")));

  auto next = connectTo(audiod.socket());
  // the configuration died with the first client, so there is nothing to capture
  sendFrame(next.get(), captureStart("b"));
  CHECK(expectFailed(next.get(), "b") == "unconfigured");
  expectNoFrame(next.get());

  sendFrame(next.get(), configureCapture(wav));
  expectReady(next.get());
  sendFrame(next.get(), captureStart("c"));
  const auto mic = expectMic(next.get());
  CHECK(mic.session == "c");
  CHECK(mic.seq == 0);
}

namespace {

// scripted source for the tests that need to place packets and failures in time
class ScriptedSource final : public PcmSource {
 public:
  struct Script {
    // whole frames handed out at once, before anything else
    Script& frames(std::size_t count) { immediate = count; return *this; }
    Script& slow(std::chrono::milliseconds wait) { slowFrame = wait; return *this; }
    Script& failing() { failRead = true; return *this; }
    Script& openDelayed(std::chrono::milliseconds wait) { openDelay = wait; return *this; }
    Script& openFails(CaptureErrorCode error) { openError = error; return *this; }
    std::size_t immediate = 0;
    // then one frame after this wait, which a stop cannot cut short
    std::chrono::milliseconds slowFrame{0};
    // after the frames: fail the read, else idle
    bool failRead = false;
    std::chrono::milliseconds openDelay{0};
    std::optional<CaptureErrorCode> openError;
  };

  explicit ScriptedSource(Script script) : script_(script) {}

  Result<CaptureFormat, CaptureError> open(const CaptureFormat& requested) override {
    std::this_thread::sleep_for(script_.openDelay);
    if (script_.openError)
      return CaptureError{*script_.openError, "scripted"};
    return requested;
  }

  CaptureRead read(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) override {
    if (given_ < script_.immediate || (script_.slowFrame > 0ms && given_ == script_.immediate)) {
      if (given_ == script_.immediate)
        std::this_thread::sleep_for(script_.slowFrame);
      ++given_;
      std::fill(interleaved.begin(), interleaved.end(), std::int16_t{100});
      return CaptureRead{CaptureRead::Kind::Frames, interleaved.size(), std::nullopt};
    }
    if (script_.failRead)
      return CaptureRead{CaptureRead::Kind::Failed, 0, CaptureError{CaptureErrorCode::ReadFailed, "scripted"}};
    std::this_thread::sleep_for(timeout);
    return CaptureRead{};
  }

  void close() override {}

 private:
  Script script_;
  std::size_t given_ = 0;
};

// the nth session's source follows the nth script
CaptureFactory scripted(std::vector<ScriptedSource::Script> scripts) {
  auto next = std::make_shared<std::size_t>(0);
  return [scripts = std::move(scripts), next](const CaptureConfig&) -> Result<std::unique_ptr<PcmSource>, std::string_view> {
    return std::unique_ptr<PcmSource>(std::make_unique<ScriptedSource>(scripts.at(std::min((*next)++, scripts.size() - 1))));
  };
}

AudiodHarness scriptedAudod(std::vector<ScriptedSource::Script> scripts) {
  return AudiodHarness(compiledBackends(), {}, openOutput, scripted(std::move(scripts)));
}

}  // namespace

TEST_CASE("capture: a packet an ended session posted is never sent as the new one", "[capture]") {
  // a's third frame is still being read when b starts, so it reaches the loop after b began
  auto audiod = scriptedAudod({ScriptedSource::Script().frames(2).slow(150ms), ScriptedSource::Script().frames(3)});
  auto client = connectConfigured(audiod, "unused");
  sendFrame(client.get(), captureStart("a"));
  CHECK(expectMic(client.get()).session == "a");
  CHECK(expectMic(client.get()).session == "a");
  sendFrame(client.get(), captureStart("b"));
  for (std::uint32_t seq = 0; seq < 3; ++seq) {
    const auto mic = expectMic(client.get());
    CHECK(mic.session == "b");
    CHECK(mic.seq == seq);
  }
  expectNoFrame(client.get());
}

TEST_CASE("capture: a failure is reported once to cadenced with its reason", "[capture]") {
  SECTION("source cannot open") {
    auto audiod = scriptedAudod({ScriptedSource::Script().openFails(CaptureErrorCode::OpenFailed)});
    auto client = connectConfigured(audiod, "unused");
    sendFrame(client.get(), captureStart("s1"));
    CHECK(expectFailed(client.get(), "s1") == "open_failed");
    expectNoFrame(client.get());
  }
  SECTION("source format is not 16 kHz mono") {
    auto audiod = scriptedAudod({ScriptedSource::Script().openFails(CaptureErrorCode::FormatMismatch)});
    auto client = connectConfigured(audiod, "unused");
    sendFrame(client.get(), captureStart("s1"));
    CHECK(expectFailed(client.get(), "s1") == "format");
  }
  SECTION("missing file") {
    AudiodHarness audiod;
    auto client = connectConfigured(audiod, audiod.file("missing.wav"));
    sendFrame(client.get(), captureStart("s1"));
    CHECK(expectFailed(client.get(), "s1") == "open_failed");
  }
  SECTION("read fails after some audio") {
    auto audiod = scriptedAudod({ScriptedSource::Script().frames(2).failing()});
    auto client = connectConfigured(audiod, "unused");
    sendFrame(client.get(), captureStart("s1"));
    CHECK(expectMic(client.get()).seq == 0);
    CHECK(expectMic(client.get()).seq == 1);
    CHECK(expectFailed(client.get(), "s1") == "read_failed");
    expectNoFrame(client.get());
    expectPong(client.get(), 1);
  }
}

TEST_CASE("capture: the failure of a superseded session is not reported", "[capture]") {
  auto audiod = scriptedAudod({ScriptedSource::Script().openDelayed(150ms).openFails(CaptureErrorCode::OpenFailed), ScriptedSource::Script().frames(2)});
  auto client = connectConfigured(audiod, "unused");
  sendFrame(client.get(), captureStart("a"));
  sendFrame(client.get(), captureStart("b"));
  CHECK(expectMic(client.get()).session == "b");
  CHECK(expectMic(client.get()).session == "b");
  expectNoFrame(client.get(), 400ms);
}
