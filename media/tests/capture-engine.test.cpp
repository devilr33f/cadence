#include "audio/opus-codec.h"
#include "audio/wake-gate.h"
#include "audiod/capture.h"
#include "audiod/media-sessions.h"
#include "audiod/mic-layout.h"
#include "audiod/output.h"
#include "audiod/vendor-probe.h"
#include "file/wav.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/engine-harness.h"
#include "support/eventually.h"
#include "support/signals.h"
#include "sync/clock-socket.h"
#include "trace/trace.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <future>
#include <memory>
#include <numbers>
#include <numeric>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <thread>
#include <utility>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

constexpr auto kAlsa = cadence_audiod_v1_Backend_BACKEND_ALSA;
constexpr auto kFile = cadence_audiod_v1_Backend_BACKEND_FILE;
// the fake vqe's chunk: 10 ms at 48 kHz, 160 samples of 16 kHz output
constexpr std::size_t kChunk = 480;
constexpr std::size_t kChannels = 4;
constexpr double kTone = 1000;
// a decoded tone at 0.25 keeps most of its 8192; one that never went in stays far below
constexpr double kHeard = 5000;
constexpr double kUnheard = 300;

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  REQUIRE(text.size() < N);
  std::memcpy(out, text.data(), text.size());
  out[text.size()] = '\0';
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

Envelope muteState(bool muted) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_mute_state_tag;
  envelope.body.mute_state.muted = muted;
  return envelope;
}

// every frame until none came for quiet
std::vector<Envelope> drain(int fd, std::chrono::milliseconds quiet = 300ms) {
  std::vector<Envelope> frames;
  for (;;) {
    const auto received = receive(fd, quiet);
    if (received.kind != Received::Kind::frame) {
      REQUIRE(received.kind == Received::Kind::timeout);
      return frames;
    }
    frames.push_back(received.envelope);
  }
}

std::vector<Envelope> only(const std::vector<Envelope>& frames, pb_size_t body) {
  std::vector<Envelope> kept;
  std::copy_if(frames.begin(), frames.end(), std::back_inserter(kept),
               [body](const Envelope& envelope) { return envelope.which_body == body; });
  return kept;
}

// the packets decoded at 48 kHz, one after the other
std::vector<std::int16_t> decoded(const std::vector<Envelope>& mics) {
  OpusDecoder decoder;
  std::vector<std::int16_t> samples;
  for (const auto& envelope : mics) {
    const auto& mic = envelope.body.mic_opus;
    const auto frame = decoder.decode(std::span<const std::uint8_t>(mic.opus.bytes, mic.opus.size));
    REQUIRE(frame.ok());
    samples.insert(samples.end(), frame.value().begin(), frame.value().end());
  }
  return samples;
}

// the tone's amplitude past the codec's start-up, over whole cycles
double tone(const std::vector<std::int16_t>& samples) {
  constexpr std::size_t kSkip = 2 * kDecodedFrameSamples;
  REQUIRE(samples.size() >= kSkip + 4800);
  const auto window = (samples.size() - kSkip) / 48 * 48;
  return amplitudeAt(samples, 1, 0, kSkip, window, kTone, 48000);
}

std::vector<std::uint32_t> seqs(const std::vector<Envelope>& mics, std::string_view session) {
  std::vector<std::uint32_t> out;
  for (const auto& envelope : mics) {
    CHECK(std::string_view(envelope.body.mic_opus.session_id) == session);
    out.push_back(envelope.body.mic_opus.seq);
  }
  return out;
}

std::vector<std::uint32_t> commandIds(const std::vector<Envelope>& frames) {
  std::vector<std::uint32_t> ids;
  for (const auto& envelope : only(frames, cadence_audiod_v1_Envelope_command_word_tag))
    ids.push_back(envelope.body.command_word.id);
  return ids;
}

// the device the capture part opens, each time: the rig's scripted source behind it
class SharedSource final : public PcmSource {
 public:
  explicit SharedSource(PcmSource& source) : source_(source) {}
  Result<CaptureFormat, CaptureError> open(const CaptureFormat& requested) override { return source_.open(requested); }
  CaptureRead read(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) override {
    return source_.read(interleaved, timeout);
  }
  void close() override { source_.close(); }

 private:
  PcmSource& source_;
};

struct RigOptions {
  // what the fake probe child does: <quick_exit|abort|hang>@<stock symbol>, or nothing
  std::string script{};
  std::uint64_t warmupMs = 0;
  std::chrono::milliseconds probeLimit = kProbeLimit;
  bool library = true;
  bool activationModel = true;
  // the command model's dir is on the device
  bool commandModel = true;
  // the real cadence-audiod in probe mode instead of the fake probe child
  bool realProbe = false;
  // how soon after a wake a session takes the audio heard since it
  std::chrono::milliseconds wakePreroll = kWakePreroll;
  // how long each fake probe child waits before it probes
  std::chrono::milliseconds probeDelay{0};
  // a stock call the fake probe child's library fails cleanly, by the fake's failure name
  std::string fail{};
  // how long the engines may take to start once their probes passed
  std::chrono::milliseconds startLimit = kEngineStartLimit;
  // the fake preset's channel counts; the usual fixture has three microphones and one reference
  int probeMics = 3;
  int probeSpeakers = 1;
};

// the policy words a board sends, in their order: CommandWord ids index this list
const std::vector<std::string> kWords{"стоп", "хватит", "пауза", "тише", "громче"};

// audiod with the fake stock library in its own process, the fake probe child (support/fake-vendor-probe.cpp) and
// a scripted capture device. while the engines run, vqe output 9 (stt) carries a 1 kHz tone at 0.25 whatever the
// device captures, so a MicOpus holding the tone over silent capture came from the vqe
class Rig : public engine_harness::EngineFixture {
 public:
  explicit Rig(RigOptions options = {}) : options_(std::move(options)) {
    std::filesystem::create_directories(stock_ / "libs");
    if (options_.library)
      std::ofstream(stock_ / "libs" / "libvendor.so").put('\0');
    if (options_.activationModel)
      std::filesystem::create_directories(stock_ / "models" / "activation");
    if (options_.commandModel)
      std::filesystem::create_directories(command_);
    stock.commandModel = command_;
    duringProcess = [this] {
      auto& stt = stock.outputs[fake_stock::kOmniWithGain];
      const auto first = processed_.load() * stt.size();
      for (std::size_t index = 0; index < stt.size(); ++index)
        stt[index] = static_cast<float>(
            0.25 * std::sin(2 * std::numbers::pi * kTone * static_cast<double>(first + index) / 16000));
      ++processed_;
    };
    VendorOptions vendor{
        .loader = fake_stock::loadFake,
        .probe = options_.realProbe
                     ? ProbeProgram{.path = CADENCE_AUDIOD_BINARY, .env = {}}
                     : ProbeProgram{.path = CADENCE_FAKE_VENDOR_PROBE,
                                    .env = {"CADENCE_FAKE_PROBE=" + options_.script,
                                            "CADENCE_FAKE_COMMAND_MODEL=" + command_.string(),
                                            "CADENCE_FAKE_PROBE_LOG=" + probeLog_.string(),
                                            "CADENCE_FAKE_PROBE_DELAY_MS=" + std::to_string(options_.probeDelay.count()),
                                            "CADENCE_FAKE_FAIL=" + options_.fail,
                                            "CADENCE_FAKE_PROBE_MICS=" + std::to_string(options_.probeMics),
                                            "CADENCE_FAKE_PROBE_SPEAKERS=" + std::to_string(options_.probeSpeakers)}},
        .probeLimit = options_.probeLimit,
        .wakeWarmupMs = options_.warmupMs,
        .wakePreroll = options_.wakePreroll,
        .startLimit = options_.startLimit,
        .exit = [this](int code) { exitCode_.store(code); }};
    audiod_ = std::make_unique<AudiodHarness>(
        std::vector<Backend>{kAlsa, kFile}, std::string{}, openOutput,
        [this](const CaptureConfig& config) -> Result<std::unique_ptr<PcmSource>, std::string_view> {
          if (config.backend == kFile)
            return openCapture(config);
          return std::unique_ptr<PcmSource>(std::make_unique<SharedSource>(source));
        },
        HarnessMedia{}, std::move(vendor));
    client_ = connectTo(audiod_->socket());
  }

  [[nodiscard]] int client() const { return client_.get(); }
  [[nodiscard]] const AudiodHarness& audiod() const { return *audiod_; }
  [[nodiscard]] std::filesystem::path file(std::string_view name) const { return dir_.file(name); }

  // v1's four-mic test layout, as a board would send it
  [[nodiscard]] Envelope configureEnvelope(bool enabled = true, bool commandModel = true,
                                           cadence_audiod_v1_Backend backend = kAlsa) const {
    Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
    envelope.which_body = cadence_audiod_v1_Envelope_configure_tag;
    auto& configure = envelope.body.configure;
    configure.has_capture = true;
    auto& capture = configure.capture;
    capture.backend = backend;
    copyText(capture.device, "test");
    capture.sample_rate = 48000;
    capture.channels = kChannels;
    capture.period_frames = 256;
    capture.buffer_frames = 8192;
    capture.has_layout = true;
    capture.layout = {.channels = kChannels,
                      .mic_channels_count = 3,
                      .mic_channels = {0, 1, 2},
                      .reference_channels_count = 1,
                      .reference_channels = {3},
                      .sample_rate = 48000};
    capture.has_engines = true;
    auto& engines = capture.engines;
    engines.enabled = enabled;
    engines.stock_roots_count = 2;
    copyText(engines.stock_roots[0], dir_.file("absent"));
    copyText(engines.stock_roots[1], stock_.string());
    copyText(engines.library, "libs/libvendor.so");
    copyText(engines.activation_model, "models/activation");
    if (commandModel) {
      copyText(engines.command_model, command_.string());
      for (const auto& word : kWords)
        copyText(engines.command_words[engines.command_words_count++], word);
    }
    copyText(engines.vqe_preset, "board");
    return envelope;
  }

  // Configure, its Ready and media state, then the vendor status that follows
  cadence_audiod_v1_VendorStatus configure(bool enabled = true, bool commandModel = true) {
    sendFrame(client(), configureEnvelope(enabled, commandModel));
    expectReady(client());
    return expectStatus();
  }

  cadence_audiod_v1_VendorStatus expectStatus() {
    const auto received = receive(client(), 10s);
    REQUIRE(received.kind == Received::Kind::frame);
    REQUIRE(received.envelope.which_body == cadence_audiod_v1_Envelope_vendor_status_tag);
    return received.envelope.body.vendor_status;
  }

  // applied once the pong is back: the loop handles frames in order
  void mute(bool muted, std::uint64_t seq) {
    sendFrame(client(), muteState(muted));
    expectPong(client(), seq);
  }

  // silent capture until the engines handled it all, in batches well under the engine's one-second fifo
  void feed(std::size_t chunks) {
    constexpr std::size_t kBatch = 50;
    for (std::size_t left = chunks; left > 0;) {
      const auto queued = std::min(left, kBatch);
      const auto target = processed_.load() + queued;
      source.pushFrames(std::vector<std::int16_t>(queued * kChunk * kChannels, 0));
      REQUIRE(eventually([&] { return processed_.load() >= target; }));
      left -= queued;
    }
  }

  // a 1 kHz tone at 0.25 in the given channels, silence in the others; the raw capture reads it all
  void feedTone(std::size_t frames, const std::vector<std::size_t>& channels,
                std::size_t captureChannels = kChannels) {
    source.pushFrames(fake_pcm::captureFrames(frames, captureChannels, [&](std::size_t frame, std::size_t channel) {
      if (std::find(channels.begin(), channels.end(), channel) == channels.end())
        return std::int16_t{0};
      return static_cast<std::int16_t>(
          std::lround(8192 * std::sin(2 * std::numbers::pi * kTone * static_cast<double>(frame) / 48000)));
    }));
    REQUIRE(eventually([&] { return source.drained(); }));
  }

  // the probe children in order: main, then commands
  [[nodiscard]] std::vector<std::string> probes() const {
    std::vector<std::string> runs;
    std::ifstream in(probeLog_);
    for (std::string line; std::getline(in, line);)
      runs.push_back(line);
    return runs;
  }

  [[nodiscard]] std::filesystem::path activationModel() const { return stock_ / "models" / "activation"; }

  // the code audiod would have exited with, -1 while it runs
  [[nodiscard]] int exitCode() const { return exitCode_.load(); }

 private:
  RigOptions options_;
  std::atomic<int> exitCode_{-1};
  TempDir dir_;
  std::filesystem::path stock_ = dir_.file("stock");
  std::filesystem::path command_ = dir_.file("command");
  std::filesystem::path probeLog_ = dir_.file("probes.log");
  std::atomic<std::size_t> processed_{0};
  std::unique_ptr<AudiodHarness> audiod_;
  UniqueFd client_;
};

const std::vector<std::string> kBothProbes{"main", "commands"};

}  // namespace

TEST_CASE("capture engines: a spotter hit is a WakeEvent once the warm-up after the capture opened passed",
          "[capture-engine]") {
  Rig rig({.warmupMs = kWakeWarmupMs});
  // 1 s and 3.5 s into the capture
  rig.wakeChunks = {100, 350};
  const auto status = rig.configure();
  CHECK(status.engines);
  CHECK(std::string_view(status.code).empty());

  const auto before = unixNowMs();
  rig.feed(360);
  const auto wakes = only(drain(rig.client()), cadence_audiod_v1_Envelope_wake_event_tag);
  REQUIRE(wakes.size() == 1);
  const auto& wake = wakes.front().body.wake_event;
  CHECK(std::string_view(wake.engine) == "stock");
  CHECK(wake.score == 1.0f);
  CHECK(wake.at_ms >= before);
  CHECK(wake.at_ms <= unixNowMs());
  CHECK(wake.at_unix_ns / 1'000'000 == wake.at_ms);
  CHECK(rig.probes() == kBothProbes);
}

TEST_CASE("capture engines: a wake is marked WAKE_DETECTED at its WakeEvent's instant, just before it",
          "[capture-engine]") {
  Rig rig({.warmupMs = kWakeWarmupMs});
  rig.wakeChunks = {350};
  REQUIRE(rig.configure().engines);

  const auto before = trace::unixNowNs();
  rig.feed(360);
  std::vector<Envelope> frames;
  for (;;) {
    const auto received = receive(rig.client(), 300ms, true);
    if (received.kind != Received::Kind::frame)
      break;
    frames.push_back(received.envelope);
  }
  const auto wakes = only(frames, cadence_audiod_v1_Envelope_wake_event_tag);
  REQUIRE(wakes.size() == 1);
  const auto& wake = wakes.front().body.wake_event;
  CHECK(wake.at_unix_ns >= before);
  CHECK(wake.at_unix_ns <= trace::unixNowNs());
  const auto marks = only(frames, cadence_audiod_v1_Envelope_trace_mark_tag);
  if constexpr (!trace::kEnabled) {
    CHECK(marks.empty());
    return;
  }
  REQUIRE(marks.size() == 1);
  const auto& mark = marks.front().body.trace_mark;
  CHECK(mark.stage == cadence_audiod_v1_TraceStage_TRACE_STAGE_WAKE_DETECTED);
  CHECK(mark.at_unix_ns == wake.at_unix_ns);
  CHECK(std::string_view(mark.session_id).empty());
  CHECK(std::string_view(mark.stream_id).empty());
  REQUIRE(mark.values_count == 1);
  CHECK(std::string_view(mark.values[0].key) == "score");
  CHECK(mark.values[0].value == 1.0);
  // the mark first, so cadenced holds it when the wake's turn opens
  const auto at = [&](pb_size_t body) {
    return std::find_if(frames.begin(), frames.end(), [body](const Envelope& e) { return e.which_body == body; });
  };
  CHECK(at(cadence_audiod_v1_Envelope_trace_mark_tag) < at(cadence_audiod_v1_Envelope_wake_event_tag));
}

TEST_CASE("capture engines: a hit while muted is no WakeEvent, and unmuting starts the warm-up again",
          "[capture-engine]") {
  Rig rig({.warmupMs = kWakeWarmupMs});
  // 3.1 s; 4 s while muted; 6 s, 1.5 s after the unmute; 8 s, 3.5 s after it
  rig.wakeChunks = {310, 400, 600, 800};
  REQUIRE(rig.configure().engines);
  rig.feed(320);
  CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_wake_event_tag).size() == 1);

  rig.mute(true, 1);
  rig.feed(130);
  CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_wake_event_tag).empty());

  rig.mute(false, 2);
  rig.feed(200);
  CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_wake_event_tag).empty());
  rig.feed(160);
  CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_wake_event_tag).size() == 1);
}

TEST_CASE("capture engines: MicOpus carries the vqe output of an open session only, and stops while muted",
          "[capture-engine]") {
  // no pre-roll: the session starts with what follows its own start
  Rig rig({.wakePreroll = 0ms});
  rig.wakeChunks = {5};
  REQUIRE(rig.configure().engines);

  // a wake goes out with no session open, and no microphone with it
  rig.feed(50);
  auto frames = drain(rig.client());
  CHECK(only(frames, cadence_audiod_v1_Envelope_wake_event_tag).size() == 1);
  CHECK(only(frames, cadence_audiod_v1_Envelope_mic_opus_tag).empty());

  sendFrame(rig.client(), captureStart("s1"));
  expectPong(rig.client(), 1);
  rig.feed(100);
  auto mics = only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag);
  REQUIRE(mics.size() == 50);
  std::vector<std::uint32_t> expected(50);
  std::iota(expected.begin(), expected.end(), 0U);
  CHECK(seqs(mics, "s1") == expected);
  CHECK(tone(decoded(mics)) > kHeard);

  sendFrame(rig.client(), muteState(true));
  drain(rig.client());
  rig.feed(50);
  CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag).empty());

  // the session is still open, so its microphone comes back, numbered on
  rig.mute(false, 2);
  rig.feed(50);
  mics = only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag);
  REQUIRE(mics.size() == 25);
  CHECK(mics.front().body.mic_opus.seq == 50);

  sendFrame(rig.client(), captureStop("s1"));
  drain(rig.client());
  rig.feed(50);
  CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag).empty());
}

TEST_CASE("capture engines: a command word goes out as its index in the configured words, never as the phrase",
          "[capture-engine]") {
  Rig rig;
  // one command utterance per 1.2 s refractory window; the spotter may pad what it reports
  rig.commandChunks = {{10, "стоп"}, {150, "кукареку"}, {300, " тише\n"}, {450, "громче"}};
  const auto status = rig.configure();
  CHECK(status.engines);
  CHECK(std::string_view(status.code).empty());
  CHECK(rig.probes() == kBothProbes);

  rig.feed(310);
  const auto frames = drain(rig.client());
  CHECK(commandIds(frames) == std::vector<std::uint32_t>{0, 3});
  CHECK(rig.audiod().logs().count(R"("msg":"command_unknown","count":1)") == 1);
  CHECK(only(frames, cadence_audiod_v1_Envelope_wake_event_tag).empty());
  const auto command = only(frames, cadence_audiod_v1_Envelope_command_word_tag).front().body.command_word;
  CHECK(command.score == 1.0f);
  CHECK(command.at_ms > 0);

  rig.mute(true, 1);
  rig.feed(150);
  CHECK(commandIds(drain(rig.client())).empty());
  for (const auto& phrase : {"стоп", "тише", "громче", "кукареку"})
    CHECK(rig.audiod().logs().count(phrase) == 0);
}

TEST_CASE("capture engines: a command model the probe sees fail leaves the wake on and command words off",
          "[capture-engine]") {
  Rig rig({.script = "quick_exit@spotter_create_from_multi_conf"});
  rig.wakeChunks = {10};
  const auto status = rig.configure();
  CHECK(status.engines);
  CHECK(std::string_view(status.code) == "command_model_failed");
  CHECK(rig.probes() == kBothProbes);
  // audiod built its spotter without the command model
  CHECK(rig.stock.confReads == std::vector<std::string>{rig.activationModel().string()});

  rig.feed(20);
  CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_wake_event_tag).size() == 1);
}

TEST_CASE("capture engines: without the command model the wake runs alone, saying why when one was named",
          "[capture-engine]") {
  SECTION("named, but not on the device") {
    Rig rig({.commandModel = false});
    const auto status = rig.configure();
    CHECK(status.engines);
    CHECK(std::string_view(status.code) == "command_model_missing");
    CHECK(rig.probes() == std::vector<std::string>{"main"});
  }
  SECTION("not named") {
    Rig rig;
    const auto status = rig.configure(true, false);
    CHECK(status.engines);
    CHECK(std::string_view(status.code).empty());
    CHECK(rig.probes() == std::vector<std::string>{"main"});
  }
  SECTION("named, but with no words to listen for") {
    Rig rig;
    auto configure = rig.configureEnvelope();
    configure.body.configure.capture.engines.command_words_count = 0;
    sendFrame(rig.client(), configure);
    expectReady(rig.client());
    const auto status = rig.expectStatus();
    CHECK(status.engines);
    CHECK(std::string_view(status.code).empty());
    CHECK(rig.probes() == std::vector<std::string>{"main"});
  }
}

TEST_CASE("capture engines: a session opened soon after a wake starts with up to 300 ms heard since it",
          "[capture-engine]") {
  Rig rig({.wakePreroll = 1h});
  // three wakes, each past the refractory window of the one before
  rig.wakeChunks = {5, 200, 400};
  REQUIRE(rig.configure().engines);

  // 50 ms since the wake: two whole 20 ms frames, and half a frame the session completes
  rig.feed(10);
  CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag).empty());
  sendFrame(rig.client(), captureStart("s1"));
  expectPong(rig.client(), 1);
  rig.feed(20);
  auto mics = only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag);
  std::vector<std::uint32_t> expected(12);
  std::iota(expected.begin(), expected.end(), 0U);
  CHECK(seqs(mics, "s1") == expected);
  CHECK(tone(decoded(mics)) > kHeard);

  // 450 ms since the wake: only the last 300 ms, 15 frames
  sendFrame(rig.client(), captureStop("s1"));
  drain(rig.client());
  rig.feed(215);
  CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag).empty());
  sendFrame(rig.client(), captureStart("s2"));
  expectPong(rig.client(), 2);
  rig.feed(20);
  mics = only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag);
  CHECK(mics.size() == 25);

  // a mute in between forgets what was heard before it
  sendFrame(rig.client(), captureStop("s2"));
  drain(rig.client());
  rig.feed(145);
  drain(rig.client());
  rig.mute(true, 3);
  rig.mute(false, 4);
  sendFrame(rig.client(), captureStart("s3"));
  expectPong(rig.client(), 5);
  rig.feed(20);
  mics = only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag);
  CHECK(mics.size() == 10);
}

TEST_CASE("capture engines: a mute switch flipped while the probe runs holds once the engines are up",
          "[capture-engine]") {
  Rig rig({.probeDelay = 300ms});
  rig.wakeChunks = {5};
  sendFrame(rig.client(), rig.configureEnvelope());
  expectReady(rig.client());
  rig.mute(true, 1);
  REQUIRE(rig.expectStatus().engines);

  sendFrame(rig.client(), captureStart("s1"));
  expectPong(rig.client(), 2);
  rig.feed(30);
  auto frames = drain(rig.client());
  CHECK(only(frames, cadence_audiod_v1_Envelope_wake_event_tag).empty());
  CHECK(only(frames, cadence_audiod_v1_Envelope_mic_opus_tag).empty());

  rig.mute(false, 3);
  rig.feed(30);
  CHECK_FALSE(only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag).empty());
}

TEST_CASE("capture engines: a spotter that fails cleanly keeps the vqe for button sessions, without a wake",
          "[capture-engine]") {
  Rig rig({.fail = "spotter-create"});
  // the spotter audiod builds fails the same way
  rig.stock.failures["spotter-create"] = "scripted";
  rig.wakeChunks = {5};
  const auto status = rig.configure();
  CHECK(status.engines);
  CHECK(std::string_view(status.code) == "spotter_failed");
  CHECK(rig.probes() == std::vector<std::string>{"main"});

  rig.feed(20);
  CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_wake_event_tag).empty());
  sendFrame(rig.client(), captureStart("button"));
  expectPong(rig.client(), 1);
  rig.feed(50);
  const auto mics = only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag);
  REQUIRE(mics.size() == 25);
  CHECK(tone(decoded(mics)) > kHeard);
}

TEST_CASE("capture engines: engines off or unusable mean raw capture of the mic channels and the reason",
          "[capture-engine]") {
  struct Case {
    RigOptions options{};
    bool enabled = true;
    std::string_view code{};
    std::vector<std::string> probes{};
  };
  const Case cases[] = {
      {.options = {}, .enabled = false, .code = "disabled", .probes = {}},
      {.options = {.library = false}, .code = "library_missing", .probes = {}},
      {.options = {.activationModel = false}, .code = "model_missing", .probes = {}},
      {.options = {.script = "abort@YandexVqeC_Config_CreateFromPreset"}, .code = "probe_failed", .probes = {"main"}},
      {.options = {.script = "quick_exit@YandexVqeC_Create"}, .code = "probe_failed", .probes = {"main"}},
      {.options = {.script = "hang@spotter_create_multichannel", .probeLimit = 300ms},
       .code = "probe_timeout",
       .probes = {"main"}},
      // the real binary's probe mode, which cannot load an empty file as the library
      {.options = {.realProbe = true}, .code = "probe_failed", .probes = {}},
  };
  for (const auto& entry : cases) {
    DYNAMIC_SECTION(entry.code << (entry.options.realProbe ? " (cadence-audiod)" : "") << " " << entry.options.script) {
      Rig rig(entry.options);
      const auto status = rig.configure(entry.enabled);
      CHECK_FALSE(status.engines);
      CHECK(std::string_view(status.code) == entry.code);
      CHECK(rig.probes() == entry.probes);
      // audiod itself never loaded the library
      CHECK(rig.stock.configCreates == 0);

      sendFrame(rig.client(), captureStart("s1"));
      rig.feedTone(48000, {0, 1, 2});
      const auto mics = only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag);
      // the session's own open, read once the device handed out its audio
      CHECK(rig.source.opens == 1);
      REQUIRE(rig.source.requested.has_value());
      CHECK(rig.source.requested->sampleRate == 48000);
      CHECK(rig.source.requested->channels == kChannels);
      CHECK(rig.source.requested->periodFrames == 256);
      CHECK(rig.source.requested->bufferFrames == 8192);
      CHECK(mics.size() >= 45);
      CHECK(mics.front().body.mic_opus.seq == 0);
      CHECK(tone(decoded(mics)) > kHeard);
    }
  }
}

TEST_CASE("capture: raw capture sends the mic channels at 16 kHz, never the speaker reference", "[capture-engine]") {
  Rig rig;
  CHECK(std::string_view(rig.configure(false).code) == "disabled");

  sendFrame(rig.client(), captureStart("mics"));
  rig.feedTone(48000, {0, 1, 2});
  CHECK(tone(decoded(only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag))) > kHeard);

  sendFrame(rig.client(), captureStart("reference"));
  rig.feedTone(48000, {3});
  const auto mics = only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag);
  REQUIRE(mics.size() >= 45);
  CHECK(std::string_view(mics.front().body.mic_opus.session_id) == "reference");
  CHECK(tone(decoded(mics)) < kUnheard);

  // the mute switch silences raw capture too
  rig.mute(true, 1);
  rig.feedTone(48000, {0, 1, 2});
  CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag).empty());
}

TEST_CASE("capture engines: a probe verdict holds for the audiod run", "[capture-engine]") {
  SECTION("engines that passed start again without a probe") {
    Rig rig;
    REQUIRE(rig.configure().engines);
    REQUIRE(rig.configure().engines);
    CHECK(rig.probes() == kBothProbes);
  }
  SECTION("engines that failed stay off without a probe") {
    Rig rig({.script = "abort@YandexVqeC_Config_CreateFromPreset"});
    CHECK(std::string_view(rig.configure().code) == "probe_failed");
    CHECK(std::string_view(rig.configure().code) == "probe_failed");
    CHECK(rig.probes() == std::vector<std::string>{"main"});
  }
}

TEST_CASE("capture engines: a session started while the probe runs starts once it resolved", "[capture-engine]") {
  Rig rig({.script = "hang@YandexVqeC_Config_CreateFromPreset", .probeLimit = 500ms});
  sendFrame(rig.client(), rig.configureEnvelope());
  expectReady(rig.client());
  sendFrame(rig.client(), captureStart("early"));
  const auto status = rig.expectStatus();
  CHECK(std::string_view(status.code) == "probe_timeout");

  rig.feedTone(48000, {0, 1, 2});
  const auto mics = only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag);
  REQUIRE_FALSE(mics.empty());
  CHECK(std::string_view(mics.front().body.mic_opus.session_id) == "early");
  CHECK(mics.front().body.mic_opus.seq == 0);
}

// a library that passed its probe and then hangs while the engines start would hold capture in its probe state for
// the rest of the run: audiod exits with the crash code instead, so cadenced restarts it, counts the crash and turns
// the engines off after three. the thread that holds the engines and the device is never abandoned
TEST_CASE("capture engines: a start that hangs past its limit exits audiod", "[capture-engine]") {
  Rig rig({.startLimit = 300ms});
  std::promise<void> release;
  const auto released = release.get_future().share();
  rig.stock.onConfigCreate = [released] { released.wait(); };
  // set before the rig goes, so its teardown can join the start
  const struct Release {
    std::promise<void>& promise;
    ~Release() { promise.set_value(); }
  } guard{release};

  sendFrame(rig.client(), rig.configureEnvelope());
  expectReady(rig.client());
  REQUIRE(eventually([&] { return rig.exitCode() != -1; }));
  CHECK(rig.exitCode() == kExitEnginesHung);
  CHECK(rig.audiod().logs().count(R"("msg":"capture_engines_hung","limit_ms":300)") == 1);
  CHECK(rig.probes() == kBothProbes);
  CHECK(receive(rig.client(), 100ms).kind == Received::Kind::timeout);
}

TEST_CASE("capture engines: a start within its limit leaves audiod running", "[capture-engine]") {
  Rig rig({.startLimit = 300ms});
  rig.stock.onConfigCreate = [] { std::this_thread::sleep_for(100ms); };
  CHECK(rig.configure().engines);
  std::this_thread::sleep_for(500ms);
  CHECK(rig.exitCode() == -1);
  CHECK(rig.audiod().logs().count("capture_engines_hung") == 0);
}

#ifndef CADENCE_MEDIA_KWS
// a board's keyword section reaching an audiod built without the keyword engine (every board but the vk capsula
// mini's): raw capture, and the reason, without the vendor engines it may name beside it
TEST_CASE("capture: an audiod without the keyword engine answers a keyword section with raw capture",
          "[capture-engine]") {
  Rig rig;
  auto configure = rig.configureEnvelope();
  auto& capture = configure.body.configure.capture;
  capture.has_keyword = true;
  copyText(capture.keyword.kind, "vk-kws");
  copyText(capture.keyword.model_path, rig.file("model.bin").string());
  copyText(capture.keyword.model_sha256, std::string(64, 'a'));
  sendFrame(rig.client(), configure);
  expectReady(rig.client());
  const auto status = rig.expectStatus();
  CHECK_FALSE(status.engines);
  CHECK(std::string_view(status.code) == "keyword_unsupported");
  CHECK(rig.probes().empty());

  sendFrame(rig.client(), captureStart("s1"));
  rig.feedTone(48000, {0, 1, 2});
  const auto mics = only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag);
  CHECK(rig.source.opens == 1);
  CHECK(mics.size() >= 45);
  CHECK(tone(decoded(mics)) > kHeard);
}
#endif

TEST_CASE("capture: the file backend ignores vendor engines and sends no vendor status", "[capture-engine]") {
  Rig rig;
  const auto wav = rig.file("in.wav");
  {
    auto writer = WavWriter::create(wav, {16000, 1});
    REQUIRE(writer.ok());
    REQUIRE_FALSE(writer.value().write(std::vector<std::int16_t>(kEncoderFrameSamples * 5, 100)));
    REQUIRE_FALSE(writer.value().close());
  }
  auto configure = rig.configureEnvelope(true, true, kFile);
  auto& capture = configure.body.configure.capture;
  capture.sample_rate = 0;
  capture.channels = 0;
  capture.has_layout = false;
  copyText(capture.file_path, wav.string());
  sendFrame(rig.client(), configure);
  expectReady(rig.client());
  expectPong(rig.client(), 1);

  sendFrame(rig.client(), captureStart("sim"));
  const auto mics = only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag);
  CHECK(mics.size() == 5);
  CHECK(rig.probes().empty());
  CHECK(rig.source.opens == 0);
}

TEST_CASE("capture engines: a calibration records every microphone apart before the vqe over its window alone, with "
          "no session's packets",
          "[capture-engine]") {
  Rig rig;
  REQUIRE(rig.configure().engines);
  const auto record = [&](const std::vector<std::size_t>& toneIn, bool native = false) {
    std::promise<sync::MicResult> p;
    auto f = p.get_future();
    const auto now = sync::monoNs();
    if (native)
      rig.audiod().capture().recordNative(now + 100'000'000, now + 500'000'000,
                                          [&p](sync::MicResult r) { p.set_value(std::move(r)); });
    else
      rig.audiod().capture().record(now + 100'000'000, now + 500'000'000,
                                    [&p](sync::MicResult r) { p.set_value(std::move(r)); });
    // the engines take chunks as a device gives them: about real time, past the window's end
    for (int i = 0; i < 40; ++i) {
      rig.feedTone(2 * kChunk, toneIn);
      std::this_thread::sleep_for(20ms);
    }
    REQUIRE(f.wait_for(5s) == std::future_status::ready);
    return f.get();
  };
  // a channel's peak, of full scale
  const auto peakOf = [](const sync::MicResult& r, std::size_t channel = 0) {
    float peak = 0;
    const auto& rec = r.recording;
    for (std::size_t i = channel; i < rec.pcm.size(); i += rec.channels)
      peak = std::max(peak, std::abs(static_cast<float>(rec.pcm[i]) / 32768.0f));
    return peak;
  };
  SECTION("heard") {
    const auto r = record({0});
    REQUIRE(r.code.empty());
    // every microphone at 16 kHz, whether the capture's tap runs at that rate or faster
    CHECK(r.recording.rate == 16000);
    // the layout's three microphones, its speaker reference left out
    CHECK(r.recording.channels == 3);
    // the window's 400 ms and the 100 ms after it, in whole chunks
    CHECK(r.recording.frames() >= 6400);
    CHECK(r.recording.frames() <= 8800);
    CHECK(r.recording.stamps.back().endFrame == r.recording.frames());
    REQUIRE(r.recording.stamps.size() > 10);
    // the tone as the microphone took it (0.25 of full scale)
    CHECK(peakOf(r) > 0.2f);
    CHECK(peakOf(r) < 0.3f);
    // the engines do not stream for it: nothing goes to cadenced
    CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag).empty());
  }
  SECTION("native 48 khz for ultrasound, without the legacy resampler") {
    const auto r = record({0}, true);
    REQUIRE(r.code.empty());
    CHECK(r.recording.rate == 48000);
    CHECK(r.recording.channels == 3);
    CHECK(r.recording.frames() >= 19200);
    CHECK(r.recording.frames() <= 26400);
    CHECK(r.recording.stamps.back().endFrame == r.recording.frames());
    CHECK(peakOf(r) > 0.2f);
    CHECK(peakOf(r) < 0.3f);
    CHECK(only(drain(rig.client()), cadence_audiod_v1_Envelope_mic_opus_tag).empty());
  }
  SECTION("each microphone apart, the speaker reference left out") {
    auto r = record({1});
    REQUIRE(r.code.empty());
    REQUIRE(r.recording.channels == 3);
    CHECK(peakOf(r, 0) < 0.01f);
    CHECK(peakOf(r, 1) > 0.2f);
    CHECK(peakOf(r, 2) < 0.01f);
    r = record({3});
    REQUIRE(r.code.empty());
    for (std::size_t c = 0; c < 3; ++c)
      CHECK(peakOf(r, c) < 0.01f);
  }
  SECTION("muted") {
    rig.mute(true, 1);
    CHECK(record({0}).code == "muted");
  }
}

TEST_CASE("capture engines: a calibration keeps only the first four microphones of a wide tap", "[capture-engine]") {
  Rig rig({.probeMics = 7});
  auto configure = rig.configureEnvelope();
  auto& capture = configure.body.configure.capture;
  capture.channels = 8;
  capture.layout.channels = 8;
  capture.layout.mic_channels_count = 7;
  for (pb_size_t c = 0; c < 7; ++c)
    capture.layout.mic_channels[c] = c;
  capture.layout.reference_channels_count = 1;
  capture.layout.reference_channels[0] = 7;
  rig.stock.mics = 7;
  rig.stock.speakers = 1;
  sendFrame(rig.client(), configure);
  expectReady(rig.client());
  const auto status = rig.expectStatus();
  INFO("vendor status " << std::string_view(status.code));
  REQUIRE(status.engines);

  std::promise<sync::MicResult> p;
  auto f = p.get_future();
  const auto now = sync::monoNs();
  rig.audiod().capture().record(now + 100'000'000, now + 500'000'000,
                                [&p](sync::MicResult r) { p.set_value(std::move(r)); });
  for (int i = 0; i < 40; ++i) {
    rig.feedTone(2 * kChunk, {0, 1, 2, 3, 4, 5, 6}, 8);
    std::this_thread::sleep_for(20ms);
  }
  REQUIRE(f.wait_for(5s) == std::future_status::ready);
  const auto result = f.get();
  REQUIRE(result.code.empty());
  CHECK(result.recording.rate == 16000);
  CHECK(result.recording.channels == 4);
}

TEST_CASE("capture calibration: the microphone split keeps a 48 kHz chirp's position at 16 kHz") {
  CaptureLayout layout{.device = {},
                       .sampleRate = 48000,
                       .channels = 1,
                       .microphones = {0},
                       .speakers = {},
                       .periodFrames = 0,
                       .bufferFrames = 0};
  auto split = MicSplit::create(layout, 1);
  REQUIRE(split.ok());
  constexpr std::size_t kInputStart = 1200;
  constexpr std::size_t kInputChirp = 480;
  constexpr std::size_t kOutputStart = kInputStart / 3;
  constexpr std::size_t kOutputChirp = kInputChirp / 3;
  const auto sweep = [](std::size_t n, std::uint32_t rate) {
    constexpr double kDuration = 0.01;
    constexpr double kF0 = 800;
    constexpr double kF1 = 6000;
    const double t = static_cast<double>(n) / rate;
    const double phase = kF0 * kDuration / std::log(kF1 / kF0) *
                         (std::exp(std::log(kF1 / kF0) * t / kDuration) - 1);
    const double fade = std::min({1.0, t / 0.001, (kDuration - t) / 0.001});
    return 0.25 * fade * std::sin(2 * std::numbers::pi * phase);
  };
  std::vector<std::int16_t> input(4800, 0);
  for (std::size_t n = 0; n < kInputChirp; ++n)
    input[kInputStart + n] = static_cast<std::int16_t>(std::lround(32767 * sweep(n, 48000)));
  std::vector<std::int16_t> output;
  split.value().push(input, output);
  REQUIRE(output.size() > kOutputStart + kOutputChirp + 8);

  std::size_t best = 0;
  double bestCorrelation = -1;
  for (std::size_t start = kOutputStart - 8; start <= kOutputStart + 8; ++start) {
    double correlation = 0;
    for (std::size_t n = 0; n < kOutputChirp; ++n)
      correlation += output[start + n] * sweep(n, 16000);
    if (correlation > bestCorrelation) {
      bestCorrelation = correlation;
      best = start;
    }
  }
  CHECK(std::abs(static_cast<std::ptrdiff_t>(best) - static_cast<std::ptrdiff_t>(kOutputStart)) <= 1);
}
