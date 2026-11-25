#include "audio/opus-codec.h"
#include "audiod/capture.h"
#include "audiod/keyword-capture.h"
#include "audiod/keyword-model.h"
#include "audiod/media-sessions.h"
#include "audiod/output.h"
#include "file/wav.h"
#include "kws/frontend.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/fake-pcm.h"
#include "sync/sha256.h"

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
#include <functional>
#include <future>
#include <iterator>
#include <memory>
#include <mutex>
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
// one capture frame of the uplink and the spotter: 20 ms at 16 kHz
constexpr std::size_t kPush = kEncoderFrameSamples;
// tests/kws/tiny.tflite
constexpr std::string_view kTinySha = "aed2d328e70185474efbfc4bed807d28521051ec326ab1f7bb8602b522e7a5f5";
// what the stock model keeps of its arena
constexpr std::size_t kStockArena = 813'968;

std::string event(std::string_view msg) { return "\"msg\":\"" + std::string(msg) + "\""; }

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

std::vector<std::uint32_t> seqs(const std::vector<Envelope>& mics, std::string_view session) {
  std::vector<std::uint32_t> out;
  for (const auto& envelope : mics) {
    CHECK(std::string_view(envelope.body.mic_opus.session_id) == session);
    out.push_back(envelope.body.mic_opus.seq);
  }
  return out;
}

// each packet's loudest sample, decoded
std::vector<int> peaks(const std::vector<Envelope>& mics) {
  OpusDecoder decoder;
  std::vector<int> out;
  for (const auto& envelope : mics) {
    const auto& mic = envelope.body.mic_opus;
    const auto frame = decoder.decode(std::span<const std::uint8_t>(mic.opus.bytes, mic.opus.size));
    REQUIRE(frame.ok());
    int peak = 0;
    for (const auto sample : frame.value())
      peak = std::max(peak, std::abs(static_cast<int>(sample)));
    out.push_back(peak);
  }
  return out;
}

std::vector<std::uint32_t> counting(std::uint32_t from, std::size_t count) {
  std::vector<std::uint32_t> out(count);
  std::iota(out.begin(), out.end(), from);
  return out;
}

// pushes of noise, each its own
std::vector<std::int16_t> noise(std::size_t pushes, std::uint32_t seed) {
  std::vector<std::int16_t> samples(pushes * kPush);
  for (auto& sample : samples) {
    seed = seed * 1'664'525U + 1'013'904'223U;
    sample = static_cast<std::int16_t>(static_cast<std::int32_t>(seed >> 16) - 32768);
  }
  return samples;
}

std::vector<std::int16_t> tone(std::size_t pushes) {
  std::vector<std::int16_t> samples(pushes * kPush);
  for (std::size_t index = 0; index < samples.size(); ++index)
    samples[index] = static_cast<std::int16_t>(
        std::lround(16000 * std::sin(2 * std::numbers::pi * 1000.0 * static_cast<double>(index) / 16000)));
  return samples;
}

std::string hex(std::span<const std::uint8_t> bytes) {
  const auto digest = sync::sha256(bytes);
  constexpr std::string_view kDigits = "0123456789abcdef";
  std::string out;
  for (const auto byte : digest) {
    out.push_back(kDigits[static_cast<std::size_t>(byte >> 4)]);
    out.push_back(kDigits[static_cast<std::size_t>(byte & 0x0f)]);
  }
  return out;
}

std::vector<std::uint8_t> readBytes(const std::filesystem::path& path) {
  std::ifstream in(path, std::ios::binary);
  REQUIRE(in.good());
  return {std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
}

void writeBytes(const std::filesystem::path& path, const std::vector<std::uint8_t>& bytes) {
  std::ofstream out(path, std::ios::binary);
  out.write(reinterpret_cast<const char*>(bytes.data()), static_cast<std::streamsize>(bytes.size()));
  REQUIRE(out.good());
}

// a call's answer with the keyword at every frame
kws::Outputs keyword() {
  kws::Outputs outputs;
  for (std::size_t frame = 0; frame < kws::KwsModel::kFrames; ++frame) {
    outputs.posNeg[2 * frame + 1] = 1.0f;
    outputs.intUnint[2 * frame + 1] = 1.0f;
  }
  return outputs;
}

using Answer = std::function<Result<kws::Outputs, std::string_view>(std::size_t call)>;

// a promise kept once, by hand or when it goes, so a test that fails early never leaves a model call waiting: one
// made after the rig goes before it, and the rig's teardown can join the spotter
class Gate {
 public:
  Gate() : opened_(promise_.get_future().share()) {}
  ~Gate() { open(); }
  Gate(const Gate&) = delete;
  Gate& operator=(const Gate&) = delete;

  void open() {
    if (!std::exchange(done_, true))
      promise_.set_value();
  }
  [[nodiscard]] std::shared_future<void> opened() const { return opened_; }

 private:
  std::promise<void> promise_;
  std::shared_future<void> opened_;
  bool done_ = false;
};

// what the scripted model answers and what it and its loader saw, shared with the test
class Script {
 public:
  // the keyword at every call while set, nothing otherwise, unless an answer is set
  std::atomic<bool> firing{false};

  void setAnswer(Answer answer) {
    const std::scoped_lock lock(mutex_);
    answer_ = std::move(answer);
  }
  void setLoadError(std::string_view error) {
    const std::scoped_lock lock(mutex_);
    loadError_ = error;
  }

  [[nodiscard]] std::size_t calls() const {
    const std::scoped_lock lock(mutex_);
    return inputs_.size();
  }
  // call from 1
  [[nodiscard]] std::vector<float> input(std::size_t call) const {
    const std::scoped_lock lock(mutex_);
    REQUIRE(call >= 1);
    REQUIRE(call <= inputs_.size());
    return inputs_[call - 1];
  }
  [[nodiscard]] int loads() const {
    const std::scoped_lock lock(mutex_);
    return loads_;
  }
  [[nodiscard]] std::vector<std::uint8_t> loaded() const {
    const std::scoped_lock lock(mutex_);
    return loaded_;
  }

  Result<kws::Outputs, std::string_view> invoke(std::span<const float> features) {
    Answer answer;
    std::size_t call = 0;
    {
      const std::scoped_lock lock(mutex_);
      inputs_.emplace_back(features.begin(), features.end());
      call = inputs_.size();
      answer = answer_;
    }
    if (answer)
      return answer(call);
    return firing.load() ? keyword() : kws::Outputs{};
  }

  std::optional<std::string_view> load(std::span<const std::uint8_t> bytes) {
    const std::scoped_lock lock(mutex_);
    ++loads_;
    loaded_.assign(bytes.begin(), bytes.end());
    if (loadError_.empty())
      return std::nullopt;
    return loadError_;
  }

 private:
  mutable std::mutex mutex_;
  Answer answer_;
  std::string_view loadError_;
  std::vector<std::vector<float>> inputs_;
  int loads_ = 0;
  std::vector<std::uint8_t> loaded_;
};

class ScriptedModel final : public kws::KwsModel {
 public:
  explicit ScriptedModel(std::shared_ptr<Script> script) : script_(std::move(script)) {}
  Result<kws::Outputs, std::string_view> invoke(std::span<const float, kFrames * kMels> features) override {
    return script_->invoke(features);
  }

 private:
  std::shared_ptr<Script> script_;
};

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

// the keyword section a board sends; a test policy whose warm-up is one call, so the second call after a start, a
// re-arm or a detection is the first that can fire
struct Keyword {
  std::string kind = std::string(kKeywordKind);
  // unset: the rig's copy of the tiny model
  std::optional<std::string> path{};
  std::string sha = std::string(kTinySha);
  float pos = 0.5f;
  float sp = 0.5f;
  std::uint32_t rearm = 8;
  std::uint32_t consecutive = 1;
};

struct RigOptions {
  // how soon after a wake a session takes what was heard since it
  std::chrono::milliseconds wakePreroll = 1h;
  // the tflm runner on the file instead of the scripted model
  bool tflm = false;
  // the scripted device hands out audio far faster than real time: deep enough that nothing is dropped, but where
  // dropping is the test
  std::size_t queueFrames = 4096;
};

// audiod with a scripted capture device (vk capsula mini's: 16 kHz mono, 640-frame periods in a 2560-frame buffer)
// and the scripted model behind the keyword loader
class Rig {
 public:
  explicit Rig(RigOptions options = {}) {
    std::filesystem::copy_file(CADENCE_KWS_TINY_MODEL, model_);
    VendorOptions vendor{.wakePreroll = options.wakePreroll};
    vendor.keywordQueueFrames = options.queueFrames;
    if (!options.tflm)
      vendor.keywordModel = [script = script](
                                std::span<const std::uint8_t> bytes) -> Result<KeywordModel, std::string_view> {
        if (const auto error = script->load(bytes))
          return *error;
        return KeywordModel{.model = std::make_unique<ScriptedModel>(script), .arenaBytes = kStockArena};
      };
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

  fake_pcm::FakePcmSource source;
  std::shared_ptr<Script> script = std::make_shared<Script>();

  [[nodiscard]] int client() const { return client_.get(); }
  [[nodiscard]] const LogCapture& logs() const { return audiod_->logs(); }
  [[nodiscard]] std::string file(std::string_view name) const { return dir_.file(name); }
  [[nodiscard]] const std::string& model() const { return model_; }

  [[nodiscard]] Envelope configureEnvelope(const Keyword& keyword = {}, cadence_audiod_v1_Backend backend = kAlsa) const {
    Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
    envelope.which_body = cadence_audiod_v1_Envelope_configure_tag;
    auto& configure = envelope.body.configure;
    configure.has_capture = true;
    auto& capture = configure.capture;
    capture.backend = backend;
    copyText(capture.device, "single_plugin");
    capture.sample_rate = 16000;
    capture.channels = 1;
    capture.period_frames = 640;
    capture.buffer_frames = 2560;
    capture.has_keyword = true;
    auto& section = capture.keyword;
    copyText(section.kind, keyword.kind);
    copyText(section.model_path, keyword.path.value_or(model_));
    copyText(section.model_sha256, keyword.sha);
    section.pos_threshold = keyword.pos;
    section.sp_threshold = keyword.sp;
    section.rearm_frames = keyword.rearm;
    section.consecutive_calls = keyword.consecutive;
    return envelope;
  }

  // Configure, its Ready and media state, then the vendor status that follows
  cadence_audiod_v1_VendorStatus configure(const Keyword& keyword = {}) {
    sendFrame(client(), configureEnvelope(keyword));
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

  // silent 20 ms frames, handled by the reader once this returns
  void feed(std::size_t pushes) { feed(std::vector<std::int16_t>(pushes * kPush, 0)); }
  void feed(std::vector<std::int16_t> samples) {
    source.pushFrames(std::move(samples));
    REQUIRE(eventually([&] { return source.drained(); }));
    // the reader reads again only once it handled what it read
    const auto begun = source.reads();
    REQUIRE(eventually([&] { return source.reads() > begun; }));
  }

  // the first `kws_started` line's invoke_ms, or -1
  [[nodiscard]] long startedInvokeMs() const {
    for (const auto& line : logs().lines()) {
      const auto at = line.find(event("kws_started"));
      if (at == std::string::npos)
        continue;
      const std::string key = R"("invoke_ms":)";
      const auto value = line.find(key, at);
      REQUIRE(value != std::string::npos);
      return std::stol(line.substr(value + key.size()));
    }
    return -1;
  }

 private:
  TempDir dir_;
  std::string model_ = dir_.file("model.bin");
  std::unique_ptr<AudiodHarness> audiod_;
  UniqueFd client_;
};

std::vector<Envelope> wakesIn(const std::vector<Envelope>& frames) {
  return only(frames, cadence_audiod_v1_Envelope_wake_event_tag);
}

std::vector<Envelope> micsIn(const std::vector<Envelope>& frames) {
  return only(frames, cadence_audiod_v1_Envelope_mic_opus_tag);
}

}  // namespace

TEST_CASE("keyword capture: a missing model reports model_missing and capture stays raw", "[keyword-capture]") {
  Rig rig;
  const auto status = rig.configure({.path = rig.file("absent.bin")});
  CHECK_FALSE(status.engines);
  CHECK(std::string_view(status.code) == "model_missing");
  CHECK(rig.script->loads() == 0);
  // nothing holds the device between sessions
  CHECK(rig.source.opens == 0);

  sendFrame(rig.client(), captureStart("button"));
  expectPong(rig.client(), 1);
  rig.feed(50);
  const auto mics = micsIn(drain(rig.client()));
  CHECK(seqs(mics, "button") == counting(0, 50));
  CHECK(rig.source.opens == 1);
  CHECK(rig.script->calls() == 0);
}

TEST_CASE("keyword capture: only the allowlisted model's bytes reach the loader", "[keyword-capture]") {
  struct Case {
    std::string_view name;
    Keyword keyword{};
    std::string_view loadError{};
    std::string_view code{};
    int loads = 0;
  };
  std::string upper(kTinySha);
  std::transform(upper.begin(), upper.end(), upper.begin(), [](char c) { return static_cast<char>(std::toupper(c)); });
  const Case cases[] = {
      {.name = "the allowlisted model", .keyword = {}, .code = "keyword", .loads = 1},
      {.name = "another sha256", .keyword = {.sha = std::string(64, '0')}, .code = "model_unknown"},
      {.name = "no sha256", .keyword = {.sha = ""}, .code = "model_unknown"},
      {.name = "an uppercase sha256", .keyword = {.sha = upper}, .code = "model_unknown"},
      {.name = "another engine", .keyword = {.kind = "other-kws"}, .code = "kind_unknown"},
      {.name = "no path", .keyword = {.path = ""}, .code = "model_missing"},
      {.name = "a directory", .keyword = {.path = std::filesystem::temp_directory_path().string()},
       .code = "model_missing"},
      {.name = "the loader's model_shape", .loadError = "model_shape", .code = "model_shape", .loads = 1},
      {.name = "the loader's arena_exceeded", .loadError = "arena_exceeded", .code = "arena_exceeded", .loads = 1},
  };
  for (const auto& entry : cases) {
    DYNAMIC_SECTION(entry.name) {
      Rig rig;
      rig.script->setLoadError(entry.loadError);
      const auto status = rig.configure(entry.keyword);
      CHECK(std::string_view(status.code) == entry.code);
      CHECK(status.engines == (entry.code == "keyword"));
      CHECK(rig.script->loads() == entry.loads);
      if (entry.loads > 0)
        CHECK(rig.script->loaded() == readBytes(rig.model()));
      CHECK(rig.source.opens == (entry.code == "keyword" ? 1 : 0));
    }
  }

  SECTION("a file past 4 MiB is refused unread, whatever its sha256") {
    Rig rig;
    const auto big = rig.file("big.bin");
    const std::vector<std::uint8_t> bytes(kKeywordModelMaxBytes + 1, 0x5a);
    writeBytes(big, bytes);
    const auto status = rig.configure({.path = big, .sha = hex(bytes)});
    CHECK_FALSE(status.engines);
    CHECK(std::string_view(status.code) == "model_invalid");
    CHECK(rig.script->loads() == 0);
  }
}

TEST_CASE("keyword capture: the tflm runner loads the allowlisted model, and an allowlisted file that is no model is "
          "model_invalid",
          "[keyword-capture]") {
  Rig rig({.tflm = true});
  const auto status = rig.configure();
  CHECK(status.engines);
  CHECK(std::string_view(status.code) == "keyword");
  // the tiny model keeps 6624 bytes of arena
  rig.feed(4 * kKeywordStartedCalls + 2);
  CHECK(rig.logs().eventually(event("kws_started") + R"(,"arena_kb":7,)"));

  const auto junk = rig.file("junk.bin");
  const std::vector<std::uint8_t> bytes(1000, 0x5a);
  writeBytes(junk, bytes);
  const auto refused = rig.configure({.path = junk, .sha = hex(bytes)});
  CHECK_FALSE(refused.engines);
  CHECK(std::string_view(refused.code) == "model_invalid");
  CHECK(rig.logs().count(event("capture_keyword_stopped")) == 1);
}

TEST_CASE("keyword capture: a wake carries up to 300 ms heard after the keyword into the next session",
          "[keyword-capture]") {
  Rig rig;
  rig.script->firing = true;
  const auto status = rig.configure();
  REQUIRE(status.engines);
  CHECK(std::string_view(status.code) == "keyword");
  // the device stays open from the Configure on
  CHECK(rig.source.opens == 1);

  // the second call, at the tenth frame, fires
  const auto before = unixNowMs();
  rig.feed(10);
  auto wakes = wakesIn(drain(rig.client()));
  REQUIRE(wakes.size() == 1);
  const auto& wake = wakes.front().body.wake_event;
  CHECK(std::string_view(wake.engine) == "vk-kws");
  CHECK(wake.score == 1.0f);
  CHECK(wake.at_ms >= before);
  CHECK(wake.at_ms <= unixNowMs());
  rig.script->firing = false;
  CHECK(rig.logs().count(event("wake_heard") + R"(,"engine":"vk-kws")") == 1);

  // 40 ms after the keyword: two frames kept, then the session's own
  rig.feed(2);
  sendFrame(rig.client(), captureStart("s1"));
  expectPong(rig.client(), 1);
  rig.feed(10);
  auto mics = micsIn(drain(rig.client()));
  CHECK(seqs(mics, "s1") == counting(0, 12));
  // the session streams the capture that stays open
  CHECK(rig.source.opens == 1);

  // 500 ms after the next keyword: only the last 300 ms, 15 frames
  sendFrame(rig.client(), captureStop("s1"));
  drain(rig.client());
  rig.script->firing = true;
  bool woke = false;
  for (int round = 0; round < 10 && !woke; ++round) {
    rig.feed(4);
    woke = !wakesIn(drain(rig.client(), 100ms)).empty();
  }
  REQUIRE(woke);
  rig.script->firing = false;
  rig.feed(25);
  sendFrame(rig.client(), captureStart("s2"));
  expectPong(rig.client(), 2);
  rig.feed(10);
  mics = micsIn(drain(rig.client()));
  CHECK(seqs(mics, "s2") == counting(0, 25));
  CHECK(rig.source.opens == 1);
}

TEST_CASE("keyword capture: a wake during a session is held, and the session's end re-arms the spotter",
          "[keyword-capture]") {
  Rig rig;
  // the board's warm-up: 147 frames, 74 pushes
  REQUIRE(rig.configure({.rearm = 0}).engines);
  sendFrame(rig.client(), captureStart("turn"));
  expectPong(rig.client(), 1);

  // the spotter runs on through the turn (it fires at the 78th and the 156th frame), and nothing wakes
  rig.script->firing = true;
  rig.feed(200);
  const auto frames = drain(rig.client());
  CHECK(wakesIn(frames).empty());
  CHECK(micsIn(frames).size() == 200);
  // 19 calls to each detection (at the 78th and the 156th frame, each re-arming), 10 after the second
  CHECK(eventually([&] { return rig.script->calls() == 48; }));
  CHECK(rig.logs().eventually(event("wake_held")));

  // without the re-arm the next detection would come 34 frames after the stop; with it, 78 frames after
  sendFrame(rig.client(), captureStop("turn"));
  expectPong(rig.client(), 2);
  rig.feed(60);
  CHECK(wakesIn(drain(rig.client())).empty());
  rig.feed(30);
  CHECK(wakesIn(drain(rig.client())).size() == 1);
}

TEST_CASE("keyword capture: a detection of a turn's audio that comes after the session closed is held",
          "[keyword-capture]") {
  Rig rig;
  Gate gate;
  rig.script->setAnswer([opened = gate.opened()](std::size_t call) -> Result<kws::Outputs, std::string_view> {
    if (call == 2)
      opened.wait();
    return keyword();
  });
  REQUIRE(rig.configure().engines);
  sendFrame(rig.client(), captureStart("turn"));
  expectPong(rig.client(), 1);
  // the firing call, on frames read in the session, is under way and waits until the session has closed
  rig.feed(10);
  REQUIRE(eventually([&] { return rig.script->calls() == 2; }));
  CHECK(micsIn(drain(rig.client())).size() == 10);
  sendFrame(rig.client(), captureStop("turn"));
  expectPong(rig.client(), 2);
  gate.open();
  CHECK(rig.logs().eventually(event("wake_held")));
  CHECK(wakesIn(drain(rig.client())).empty());
  CHECK(rig.script->calls() == 2);
}

TEST_CASE("keyword capture: a stop leaves the frames still queued for the spotter unheard", "[keyword-capture]") {
  auto rig = std::make_unique<Rig>();
  const auto script = rig->script;
  auto& source = rig->source;
  Gate gate;
  std::promise<void> entered;
  const auto inside = entered.get_future();
  script->setAnswer([&entered, opened = gate.opened()](std::size_t call) -> Result<kws::Outputs, std::string_view> {
    if (call == 1) {
      entered.set_value();
      opened.wait();
    }
    return kws::Outputs{};
  });
  REQUIRE(rig->configure().engines);
  // the first call holds the spotter while ten calls' worth of frames queue behind it
  rig->feed(6);
  REQUIRE(inside.wait_for(5s) == std::future_status::ready);
  rig->feed(40);

  std::thread stopping([&rig] { rig.reset(); });
  // the reader ends at the stop's request, which reaches the spotter at the same time: no read begins any more
  REQUIRE(eventually([&] {
    const auto before = source.reads();
    std::this_thread::sleep_for(100ms);
    return source.reads() == before;
  }));
  gate.open();
  stopping.join();
  CHECK(script->calls() == 1);
}

TEST_CASE("keyword capture: mute feeds nothing to the spotter, re-arms it and drops the pre-roll",
          "[keyword-capture]") {
  SECTION("the spotter") {
    Rig rig;
    REQUIRE(rig.configure().engines);
    rig.feed(6);
    REQUIRE(eventually([&] { return rig.script->calls() == 1; }));

    rig.mute(true, 1);
    rig.feed(tone(50));
    std::this_thread::sleep_for(100ms);
    CHECK(rig.script->calls() == 1);

    // re-armed at the unmute: the next call is the sixth frame after it, and hears none of the muted tone
    rig.mute(false, 2);
    rig.feed(5);
    std::this_thread::sleep_for(100ms);
    CHECK(rig.script->calls() == 1);
    rig.feed(1);
    REQUIRE(eventually([&] { return rig.script->calls() == 2; }));
    const auto heard = rig.script->input(2);
    CHECK(std::all_of(heard.begin(), heard.end(), [](float value) { return value == -100.0f; }));
    CHECK(wakesIn(drain(rig.client())).empty());
  }

  SECTION("the pre-roll and the session") {
    Rig rig;
    rig.script->firing = true;
    REQUIRE(rig.configure().engines);
    rig.feed(10);
    REQUIRE(wakesIn(drain(rig.client())).size() == 1);
    rig.script->firing = false;

    // what was kept after the keyword goes with the mute
    rig.feed(5);
    rig.mute(true, 1);
    rig.feed(10);
    rig.mute(false, 2);
    rig.feed(3);
    sendFrame(rig.client(), captureStart("s1"));
    expectPong(rig.client(), 3);
    rig.feed(10);
    auto mics = micsIn(drain(rig.client()));
    CHECK(seqs(mics, "s1") == counting(0, 13));

    // the session lets nothing out while muted, and streams on, numbered on, after the unmute
    rig.mute(true, 4);
    rig.feed(10);
    CHECK(micsIn(drain(rig.client())).empty());
    // the device's buffer after an unmute may hold audio heard while muted: the read that saw the unmute (40 ms) and
    // the buffer's 160 ms after it go out silenced
    rig.mute(false, 5);
    rig.feed(tone(20));
    mics = micsIn(drain(rig.client()));
    CHECK(seqs(mics, "s1") == counting(13, 20));
    const auto heard = peaks(mics);
    REQUIRE(heard.size() == 20);
    CHECK(*std::max_element(heard.begin(), heard.begin() + 9) < 500);
    CHECK(*std::min_element(heard.begin() + 12, heard.end()) > 4000);
  }

  SECTION("a detection the spotter made of audio before the mute is held") {
    Rig rig;
    Gate gate;
    rig.script->setAnswer([opened = gate.opened()](std::size_t call) -> Result<kws::Outputs, std::string_view> {
      if (call == 2)
        opened.wait();
      return keyword();
    });
    REQUIRE(rig.configure().engines);
    // the firing call waits until the mute is in
    rig.feed(10);
    rig.mute(true, 1);
    gate.open();
    REQUIRE(eventually([&] { return rig.script->calls() == 2; }));
    CHECK(rig.logs().eventually(event("wake_held")));
    CHECK(wakesIn(drain(rig.client())).empty());
  }
}

TEST_CASE("keyword capture: kws_started gives the arena and the median call once, after 50 calls",
          "[keyword-capture]") {
  Rig rig;
  rig.script->setAnswer([](std::size_t) -> Result<kws::Outputs, std::string_view> {
    std::this_thread::sleep_for(3ms);
    return kws::Outputs{};
  });
  REQUIRE(rig.configure().engines);
  rig.feed(4 * (kKeywordStartedCalls - 1) + 2);
  REQUIRE(eventually([&] { return rig.script->calls() == kKeywordStartedCalls - 1; }));
  std::this_thread::sleep_for(50ms);
  CHECK(rig.logs().count(event("kws_started")) == 0);

  rig.feed(4);
  REQUIRE(rig.logs().eventually(event("kws_started") + R"(,"arena_kb":795,"invoke_ms":)"));
  const auto ms = rig.startedInvokeMs();
  CHECK(ms >= 3);
  CHECK(ms < 1000);
  rig.feed(40);
  REQUIRE(eventually([&] { return rig.script->calls() == kKeywordStartedCalls + 10; }));
  CHECK(rig.logs().count(event("kws_started")) == 1);
}

TEST_CASE("keyword capture: a model that keeps failing stops the engine after 25 calls in a row, and the open "
          "session goes on raw",
          "[keyword-capture]") {
  Rig rig;
  // one call answers among the failures: only 25 in a row stop it
  rig.script->setAnswer([](std::size_t call) -> Result<kws::Outputs, std::string_view> {
    if (call == kKeywordFailureLimit)
      return kws::Outputs{};
    return std::string_view("invoke_failed");
  });
  REQUIRE(rig.configure().engines);
  sendFrame(rig.client(), captureStart("s1"));
  expectPong(rig.client(), 1);

  rig.feed(4 * 49 + 2);
  REQUIRE(eventually([&] { return rig.script->calls() == 49; }));
  auto frames = drain(rig.client());
  CHECK(only(frames, cadence_audiod_v1_Envelope_vendor_status_tag).empty());
  CHECK(micsIn(frames).size() == 198);
  CHECK(rig.logs().count(event("kws_failed")) == 0);

  // the 25th failure in a row
  rig.feed(4);
  frames = drain(rig.client());
  const auto statuses = only(frames, cadence_audiod_v1_Envelope_vendor_status_tag);
  REQUIRE(statuses.size() == 1);
  CHECK_FALSE(statuses.front().body.vendor_status.engines);
  CHECK(std::string_view(statuses.front().body.vendor_status.code) == "invoke_failed");
  CHECK(micsIn(frames).size() == 4);
  CHECK(rig.logs().count(event("kws_failed") + R"(,"failed_calls":25)") == 1);
  CHECK(rig.logs().count(event("capture_keyword_stopped") + R"(,"calls":50,"failed_calls":49,)") == 1);

  // the session goes on through a raw capture of its own, numbered on
  rig.feed(10);
  auto mics = micsIn(drain(rig.client()));
  CHECK(seqs(mics, "s1") == counting(202, 10));
  CHECK(rig.source.opens == 2);
  CHECK(rig.script->calls() == 50);

  // and the next session is raw too
  sendFrame(rig.client(), captureStop("s1"));
  drain(rig.client());
  sendFrame(rig.client(), captureStart("s2"));
  expectPong(rig.client(), 2);
  rig.feed(5);
  mics = micsIn(drain(rig.client()));
  CHECK(seqs(mics, "s2") == counting(0, 5));
  CHECK(rig.source.opens == 3);
}

TEST_CASE("keyword capture: a spotter that falls behind loses the oldest frames, re-arms and logs once per burst",
          "[keyword-capture]") {
  std::promise<void> entered;
  const auto inside = entered.get_future();
  Rig rig({.queueFrames = kKeywordQueueFrames});
  Gate gate;
  rig.script->setAnswer(
      [&entered, opened = gate.opened()](std::size_t call) -> Result<kws::Outputs, std::string_view> {
        if (call == 1) {
          entered.set_value();
          opened.wait();
        }
        return kws::Outputs{};
      });
  REQUIRE(rig.configure().engines);

  constexpr std::size_t kPushes = 60;
  const auto audio = noise(kPushes, 7);
  const auto pushes = [&](std::size_t from, std::size_t to) {
    return std::vector<std::int16_t>(audio.begin() + static_cast<std::ptrdiff_t>(from * kPush),
                                     audio.begin() + static_cast<std::ptrdiff_t>(to * kPush));
  };
  // the first call holds the spotter while the reader reads on
  rig.feed(pushes(0, 6));
  REQUIRE(inside.wait_for(5s) == std::future_status::ready);
  rig.feed(pushes(6, kPushes));
  gate.open();
  REQUIRE(eventually([&] { return rig.script->calls() >= 2; }));

  // the queue kept the newest frames, and the spotter re-armed before them: its next call is a fresh frontend's
  // over their first six
  const auto kept = kPushes - kKeywordQueueFrames;
  kws::KwsFrontend fresh(kws::FrontendParams::defaultCapsula());
  const auto expectedFrames = fresh.push(pushes(kept, kept + 6));
  REQUIRE(expectedFrames.size() >= kws::KwsModel::kFrames);
  std::vector<float> expected;
  for (std::size_t frame = 0; frame < kws::KwsModel::kFrames; ++frame)
    expected.insert(expected.end(), expectedFrames[frame].begin(), expectedFrames[frame].end());
  CHECK(rig.script->input(2) == expected);

  // the burst ends at the first frame that finds room
  CHECK(rig.logs().count(event("kws_frames_dropped")) == 0);
  rig.feed(1);
  const auto dropped = kPushes - 6 - kKeywordQueueFrames;
  CHECK(rig.logs().eventually(event("kws_frames_dropped") + R"(,"count":)" + std::to_string(dropped) + "}"));
  CHECK(rig.logs().count(event("kws_frames_dropped")) == 1);
}

TEST_CASE("keyword capture: the file backend runs the keyword engine on the wav", "[keyword-capture]") {
  Rig rig;
  rig.script->firing = true;
  const auto wav = rig.file("in.wav");
  {
    auto writer = WavWriter::create(wav, {16000, 1});
    REQUIRE(writer.ok());
    // 300 ms: one detection, at the tenth frame, and too few after it for another
    REQUIRE_FALSE(writer.value().write(std::vector<std::int16_t>(kPush * 15, 0)));
    REQUIRE_FALSE(writer.value().close());
  }
  auto configure = rig.configureEnvelope({}, kFile);
  copyText(configure.body.configure.capture.file_path, wav);
  sendFrame(rig.client(), configure);
  expectReady(rig.client());
  const auto status = rig.expectStatus();
  CHECK(status.engines);
  CHECK(std::string_view(status.code) == "keyword");

  const auto wakes = wakesIn(drain(rig.client(), 1s));
  REQUIRE(wakes.size() == 1);
  CHECK(std::string_view(wakes.front().body.wake_event.engine) == "vk-kws");
  CHECK(rig.source.opens == 0);
}
