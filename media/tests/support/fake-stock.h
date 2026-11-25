#pragma once

#include "audio/stock-api.h"
#include "audio/stock-types.h"
#include "audio/wake-gate.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <array>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <filesystem>
#include <functional>
#include <map>
#include <memory>
#include <optional>
#include <set>
#include <string>
#include <vector>

namespace cadence::fake_stock {

namespace abi = stock_abi;

inline constexpr int kOmni = 6;
inline constexpr int kOmniWithGain = 9;
inline constexpr int kRawMainMic = 2;
inline constexpr int kBnr = 8;
// what a stock activation model may declare: omni, bnr, and raw mic
inline constexpr std::array<int, 6> kAlisaChannels{1, 0, 0, 1, 1, 0};
// by default the fake reads the command conf from here and alice's from any other dir
inline const std::filesystem::path kTestCommandModel = "/data/command";

struct FakeError {
  std::string message;
};

// a conf handed out by spotter_conf_read: the reader owns it until a multi conf takes it. kept: attach refused
// it, yet the library owns it all the same
struct FakeConf {
  enum class State { Live, Attached, Kept, Freed };
  bool command = false;
  State state = State::Live;
};

// the component a confirmation event of the combined spotter comes from; a stray one was never attached
enum class FakeComponent { Alice, Command, Stray };

struct FakeHit {
  FakeComponent component = FakeComponent::Alice;
  std::string phrase;
};

// in-memory stand-in for the board's stock library; handles are addresses of its members
struct FakeStock {
  int chunkFrames = 480;
  int outputFrames = 160;
  int inputRate = 48000;
  int outputRate = 16000;
  int mics = 3;
  int speakers = 1;
  std::map<int, std::vector<float>> outputs;

  std::array<int, 6> spotterChannels{1, 0, 0, 0, 0, 0};
  int spotterNativeRate = 16000;
  std::array<int, 6> commandChannels{1, 0, 0, 0, 0, 0};
  int commandNativeRate = 16000;
  std::deque<int> events;
  std::optional<int> endlessEvent;
  // what each confirmation event (2 or 5) heard, in order; alice once these run out
  std::deque<FakeHit> hits;
  // the dir spotter_conf_read takes for the command model
  std::filesystem::path commandModel = kTestCommandModel;

  std::set<std::string> missingSymbols;
  // stock call name -> message of the error it reports
  std::map<std::string, std::string> failures;

  // calls the device copy would crash, abort, or quick_exit on; StockFixture fails the test on any
  std::vector<std::string> violations;

  std::filesystem::path libraryPath;
  std::string preset;
  std::string vqeModels;
  // the last spotter model dir read, and every one in order
  std::string spotterModel;
  std::vector<std::string> confReads;
  int configCreates = 0;
  int processCalls = 0;
  std::vector<float> lastMics;
  std::vector<float> lastSpeakers;
  // runs inside each process call, on the calling thread
  std::function<void()> onProcess;
  // runs inside each config create, on the calling thread: a library that hangs while it builds its engines
  std::function<void()> onConfigCreate;
  int spotterCreateRate = 0;
  std::vector<int> spotterCreateTypes;
  const void* spotterCreateReserved = &spotterCreateRate;
  bool spotterCreateFlag = true;
  std::vector<std::int16_t> submitted;
  const void* submittedTo = nullptr;
  int nextEventCalls = 0;

  // multi conf api calls in order: create, attach <alice|command>, spotter, destroy, detach <alice|command>, free
  std::vector<std::string> multiCalls;
  bool multiPresetNull = false;
  bool multiLocalCa = true;
  int multiCreateRate = 0;
  std::vector<int> multiCreateTypes;
  bool multiCreateMessageNull = false;
  bool multiCreateReservedNull = false;
  bool multiCreateThreaded = true;
  unsigned multiCreateMinChunk = 1;
  int componentCalls = 0;
  int phraseCalls = 0;

  std::vector<std::unique_ptr<FakeConf>> confs;
  std::vector<std::unique_ptr<FakeError>> errors;
  // vqe errors and spotter messages handed out, and how often each was released
  std::vector<const void*> issued;
  std::map<const void*, int> frees;
  int configFrees = 0;
  int vqeFrees = 0;
  int spotterConfFrees = 0;
  int spotterDestroys = 0;

  char configHandle = 0;
  char vqeHandle = 0;
  char spotterHandle = 0;
  char multiConfHandle = 0;
  char multiSpotterHandle = 0;
  // alice's and the command words' component handles, by attach
  std::array<char, 2> componentHandles{};
  char strayComponent = 0;

  bool multiConfAlive = false;
  bool multiSpotterAlive = false;
  std::array<FakeConf*, 2> components{};
  // the hit of the latest event, if it was a confirmation: phrase 0 otherwise
  std::optional<FakeHit> currentHit;
  // the one buffer every phrase call returns; any other spotter call rewrites it
  std::array<char, 64> phraseBuffer{};

  FakeError* newError(const std::string& call) {
    const auto failure = failures.find(call);
    if (failure == failures.end())
      return nullptr;
    errors.push_back(std::make_unique<FakeError>(FakeError{failure->second}));
    return errors.back().get();
  }

  abi::VqeError* fail(const std::string& call) {
    auto* error = newError(call);
    if (error != nullptr)
      issued.push_back(error);
    return reinterpret_cast<abi::VqeError*>(error);
  }

  char* failMessage(const std::string& call) {
    auto* error = newError(call);
    if (error == nullptr)
      return nullptr;
    issued.push_back(error->message.data());
    return error->message.data();
  }

  bool everyIssuedFreedOnce() const {
    for (const void* handed : issued) {
      const auto released = frees.find(handed);
      if (released == frees.end() || released->second != 1)
        return false;
    }
    return true;
  }

  std::size_t confsWith(FakeConf::State state) const {
    return static_cast<std::size_t>(
        std::count_if(confs.begin(), confs.end(), [state](const auto& conf) { return conf->state == state; }));
  }

  // every conf read was freed, by its reader or by the multi conf that took it
  bool allConfsReleased() const { return confsWith(FakeConf::State::Freed) == confs.size(); }

  void clobberPhrase();
};

// makes stock the one the fake library serves; null serves none
void serve(FakeStock* stock);
StockLibraryResult loadFake(const std::filesystem::path& path);
// the board's preset and capture layout, with the stock files under /stock
StockAudioConfig testConfig();
// asks for the combined spotter with the command model at kTestCommandModel
void enableCommands(StockAudioConfig& config);
StockAudioConfig commandConfig();
std::vector<float> filled(int count, float value);

class StockFixture {
 public:
  // makes this fixture's stock the one the fake library serves
  StockFixture();
  ~StockFixture();

  StockFixture(const StockFixture&) = delete;
  StockFixture& operator=(const StockFixture&) = delete;

  void setOutputs(int type, float value) { stock.outputs[type] = filled(stock.outputFrames, value); }

  std::vector<std::int16_t> silence() const {
    return std::vector<std::int16_t>(static_cast<std::size_t>(stock.chunkFrames) * testConfig().capture.channels, 0);
  }

  StockError initializeError(StockAudio& audio) const {
    const auto result = audio.initialize(testConfig());
    REQUIRE_FALSE(result.ok());
    return result.error();
  }

  void initialize(StockAudio& audio) const {
    const auto info = audio.initialize(testConfig());
    REQUIRE(info.ok());
    REQUIRE_FALSE(info.value().spotterError.has_value());
  }

  // the vqe engine and a spotter started; command words may still be off
  StockAudioInfo initialized(StockAudio& audio, const StockAudioConfig& config) const {
    const auto info = audio.initialize(config);
    REQUIRE(info.ok());
    REQUIRE_FALSE(info.value().spotterError.has_value());
    return info.value();
  }

  // vqe stays usable when only the spotter fails
  StockError spotterError(StockAudio& audio) const {
    const auto info = audio.initialize(testConfig());
    REQUIRE(info.ok());
    CHECK_FALSE(info.value().spotter.has_value());
    REQUIRE(info.value().spotterError.has_value());
    return *info.value().spotterError;
  }

  cadence::VqeChunk processSilence(StockAudio& audio) const {
    const auto input = silence();
    const auto result = audio.process(input, static_cast<std::size_t>(stock.chunkFrames));
    REQUIRE(result.ok());
    return result.value();
  }

  // processes silence for this much stream time
  void pass(StockAudio& audio, std::uint64_t ms) const {
    const auto frames = ms * static_cast<std::uint64_t>(stock.inputRate) / 1000;
    const auto chunk = static_cast<std::uint64_t>(stock.chunkFrames);
    for (std::uint64_t processed = 0; processed < frames; processed += chunk)
      processSilence(audio);
  }

  // processes silence until the wake warm-up has passed, so confirmations count from here on
  void warmUp(StockAudio& audio) const { pass(audio, kWakeWarmupMs); }

  std::optional<SpotterHit> hit(StockAudio& audio) const {
    const auto result = audio.pollHit();
    REQUIRE(result.ok());
    return result.value();
  }

  bool woke(StockAudio& audio) const {
    const auto heard = hit(audio);
    return heard && heard->kind == SpotterHit::Kind::Wake;
  }

  FakeStock stock;
};

}  // namespace cadence::fake_stock
