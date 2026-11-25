#include "support/fake-stock.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>
#include <catch2/generators/catch_generators_range.hpp>
#include <catch2/matchers/catch_matchers_string.hpp>

#include <cstddef>
#include <cstdint>
#include <iterator>
#include <string>
#include <vector>

using namespace cadence;
using namespace cadence::fake_stock;
using Catch::Matchers::ContainsSubstring;

namespace {

// the first, a middle, and the last symbol each binder resolves: one path binds them all
const char* const kSpotterSymbols[] = {"spotter_conf_read", "spotter_create_multichannel", "free"};

const char* const kMultiSpotterSymbols[] = {"spotter_multi_conf_create", "spotter_create_from_multi_conf",
                                            "spotter_get_current_phrase"};

const std::vector<std::string> kAliceThenCommands = {"/stock/spotter_models/activation/main", "/data/command"};

}  // namespace

TEST_CASE_METHOD(StockFixture, "names each missing spotter symbol and keeps the vqe engine") {
  const std::string missing = GENERATE(from_range(std::begin(kSpotterSymbols), std::end(kSpotterSymbols)));
  stock.missingSymbols.insert(missing);
  StockAudio audio(loadFake);

  const auto error = spotterError(audio);

  CHECK(error.code == StockErrorCode::MissingSymbol);
  CHECK(error.detail == missing);
  CHECK(stock.spotterModel.empty());
  processSilence(audio);
  const auto wake = audio.pollHit();
  REQUIRE_FALSE(wake.ok());
  CHECK(wake.error().code == StockErrorCode::MissingSymbol);
}

TEST_CASE_METHOD(StockFixture, "frees spotter messages after copying them") {
  SECTION("conf read") {
    stock.failures["conf-read"] = "no model";
    StockAudio audio(loadFake);
    const auto error = spotterError(audio);
    CHECK(error.code == StockErrorCode::SpotterConfigFailed);
    CHECK_THAT(describe(error), ContainsSubstring("no model"));
  }
  SECTION("message alongside a successful conf read") {
    stock.failures["conf-read-note"] = "using defaults";
    StockAudio audio(loadFake);
    initialize(audio);
  }
  SECTION("create") {
    stock.failures["spotter-create"] = "rate unsupported";
    StockAudio audio(loadFake);
    const auto error = spotterError(audio);
    CHECK(error.code == StockErrorCode::SpotterCreateFailed);
    CHECK_THAT(describe(error), ContainsSubstring("rate unsupported"));
    CHECK(stock.spotterConfFrees == 1);
  }
  REQUIRE_FALSE(stock.errors.empty());
  CHECK(stock.everyIssuedFreedOnce());
}

TEST_CASE_METHOD(StockFixture, "feeds each spotter channel the vqe output stock maind gives its type") {
  setOutputs(kOmni, -0.25f);
  setOutputs(kBnr, 0.375f);
  setOutputs(kRawMainMic, 0.125f);
  std::vector<int> expectedTypes;
  std::vector<std::int16_t> expectedFrame;
  SECTION("the alisa model: omni, bnr, and raw mic") {
    stock.spotterChannels = kAlisaChannels;
    expectedTypes = {0, 3, 4};
    expectedFrame = {-8192, 12288, 4096};
  }
  SECTION("bnr and raw mic without omni") {
    stock.spotterChannels = {0, 0, 0, 1, 1, 0};
    expectedTypes = {3, 4};
    expectedFrame = {12288, 4096};
  }
  StockAudio audio(loadFake);
  CHECK(audio.lastOutput(kBnr).empty());
  const auto info = audio.initialize(testConfig());
  REQUIRE(info.ok());

  processSilence(audio);

  REQUIRE(info.value().spotter.has_value());
  CHECK(info.value().spotter->channelTypes == expectedTypes);
  CHECK(stock.spotterCreateTypes == expectedTypes);
  REQUIRE(stock.submitted.size() == static_cast<std::size_t>(stock.outputFrames) * expectedTypes.size());
  for (std::size_t frame = 0; frame < static_cast<std::size_t>(stock.outputFrames); ++frame)
    for (std::size_t channel = 0; channel < expectedTypes.size(); ++channel)
      CHECK(stock.submitted[frame * expectedTypes.size() + channel] == expectedFrame[channel]);

  // the probe tool reads each output the wrapper reads, and none it does not
  const auto frames = static_cast<std::size_t>(stock.outputFrames);
  const auto bnr = audio.lastOutput(kBnr);
  const auto raw = audio.lastOutput(kRawMainMic);
  CHECK(std::vector<std::int16_t>(bnr.begin(), bnr.end()) == std::vector<std::int16_t>(frames, 12288));
  CHECK(std::vector<std::int16_t>(raw.begin(), raw.end()) == std::vector<std::int16_t>(frames, 4096));
  CHECK(audio.lastOutput(7).empty());
}

TEST_CASE_METHOD(StockFixture, "rejects a spotter layout it cannot feed and keeps the vqe engine") {
  stock.spotterChannels = {1, 1, 0, 0, 0, 0};
  StockAudio audio(loadFake);

  const auto error = spotterError(audio);

  CHECK(error.code == StockErrorCode::SpotterLayoutUnsupported);
  CHECK(stock.spotterConfFrees == 1);
  CHECK(stock.spotterCreateTypes.empty());
  processSilence(audio);
  CHECK(stock.submitted.empty());
  const auto wake = audio.pollHit();
  REQUIRE_FALSE(wake.ok());
  CHECK(wake.error().code == StockErrorCode::SpotterLayoutUnsupported);
}

TEST_CASE_METHOD(StockFixture, "wakes once for confirmation events 2 and 5 and keeps the wake until it is polled") {
  StockAudio audio(loadFake);
  initialize(audio);
  warmUp(audio);
  CHECK_FALSE(woke(audio));
  const auto submitsBefore = audio.spotterStats().submits;
  stock.events = {1, 2, 3, 5, 0};
  processSilence(audio);
  processSilence(audio);

  CHECK(woke(audio));
  CHECK_FALSE(woke(audio));
  CHECK(audio.spotterStats().submits == submitsBefore + 2);
  CHECK(audio.spotterStats().events[2] == 1);
  CHECK(audio.spotterStats().events[5] == 1);
}

TEST_CASE_METHOD(StockFixture, "does not wake for the other events, and counts one outside the known range") {
  const auto [event, unknown] = GENERATE(table<int, int>({{0, 0}, {4, 0}, {7, 0}, {9, 0}, {-1, 1}, {10, 1}}));
  StockAudio audio(loadFake);
  initialize(audio);
  warmUp(audio);
  stock.events = {event, 2, 0};

  processSilence(audio);

  CHECK_FALSE(woke(audio));
  CHECK(stock.events == std::deque<int>{2, 0});
  CHECK(audio.spotterStats().unknownEvents == static_cast<std::uint64_t>(unknown));
}

TEST_CASE_METHOD(StockFixture, "bounds spotter event draining per chunk") {
  stock.endlessEvent = 1;
  StockAudio audio(loadFake);
  initialize(audio);

  processSilence(audio);

  CHECK(stock.nextEventCalls > 0);
  CHECK(stock.nextEventCalls <= 64);
  CHECK_FALSE(woke(audio));
}

TEST_CASE_METHOD(StockFixture, "rebuilds the alice spotter every ten minutes, so its score records are freed") {
  StockAudio audio(loadFake);
  initialize(audio);
  const auto reads = stock.confReads.size();
  const auto types = stock.spotterCreateTypes;

  pass(audio, 599'000);
  CHECK(stock.spotterDestroys == 0);
  pass(audio, 2'000);
  CHECK(stock.spotterDestroys == 1);
  CHECK(stock.confReads.size() == reads + 1);
  CHECK(stock.spotterCreateTypes == types);
  CHECK(audio.spotterStats().rebuilds == 1);

  stock.events = {2, 0};
  warmUp(audio);
  CHECK(woke(audio));
}

TEST_CASE_METHOD(StockFixture, "a failed rebuild keeps the spotter it has") {
  StockAudio audio(loadFake);
  initialize(audio);
  stock.failures["spotter-create"] = "out of memory";

  pass(audio, 601'000);

  CHECK(stock.spotterDestroys == 0);
  CHECK(audio.spotterStats().rebuilds == 0);
  CHECK(audio.spotterStats().rebuildFailures == 1);
  stock.events = {2, 0};
  processSilence(audio);
  CHECK(woke(audio));
}

// stock maind clears the combined spotter's records per component; whether it needs a rebuild is not yet known
TEST_CASE_METHOD(StockFixture, "leaves the combined spotter alone") {
  StockAudio audio(loadFake);
  initialized(audio, commandConfig());

  pass(audio, 601'000);

  CHECK(stock.multiSpotterAlive);
  CHECK(stock.spotterDestroys == 0);
  CHECK(audio.spotterStats().rebuilds == 0);
}

TEST_CASE_METHOD(StockFixture, "builds one spotter from the alisa and command confs") {
  stock.spotterChannels = kAlisaChannels;
  stock.commandChannels = kAlisaChannels;
  setOutputs(kOmni, -0.25f);
  setOutputs(kBnr, 0.375f);
  setOutputs(kRawMainMic, 0.125f);
  StockAudio audio(loadFake);

  const auto info = initialized(audio, commandConfig());

  CHECK_FALSE(info.commandError.has_value());
  REQUIRE(info.spotter.has_value());
  CHECK(info.spotter->commands);
  CHECK(info.spotter->sampleRate == 16000);
  CHECK(info.spotter->channelTypes == std::vector<int>{0, 3, 4});
  CHECK(stock.confReads == kAliceThenCommands);
  CHECK(stock.multiPresetNull);
  CHECK_FALSE(stock.multiLocalCa);
  CHECK(stock.multiCreateRate == 16000);
  CHECK(stock.multiCreateTypes == std::vector<int>{0, 3, 4});
  CHECK(stock.multiCreateMessageNull);
  CHECK(stock.multiCreateReservedNull);
  CHECK_FALSE(stock.multiCreateThreaded);
  CHECK(stock.multiCreateMinChunk == 0);
  // the multi conf owns both confs, and no single spotter is built beside it
  CHECK(stock.spotterConfFrees == 0);
  CHECK(stock.spotterCreateTypes.empty());

  processSilence(audio);

  CHECK(stock.submittedTo == &stock.multiSpotterHandle);
  REQUIRE(stock.submitted.size() == static_cast<std::size_t>(stock.outputFrames) * 3);
  CHECK(std::vector<std::int16_t>(stock.submitted.begin(), stock.submitted.begin() + 3) ==
        std::vector<std::int16_t>{-8192, 12288, 4096});

  // initializing again without command words tears the combined spotter down; the fake flags a wrong order
  initialize(audio);
  CHECK(stock.allConfsReleased());
  CHECK_FALSE(stock.multiConfAlive);
  CHECK_FALSE(stock.multiSpotterAlive);
  CHECK(stock.everyIssuedFreedOnce());
}

TEST_CASE_METHOD(StockFixture, "routes hits by component and copies the phrase") {
  StockAudio audio(loadFake);
  initialized(audio, commandConfig());
  warmUp(audio);

  stock.hits = {{FakeComponent::Command, "стоп"}};
  stock.events = {2, 0};
  processSilence(audio);
  // later calls rewrote the library's phrase buffer before the hit is polled
  processSilence(audio);
  auto heard = hit(audio);
  REQUIRE(heard.has_value());
  CHECK(heard->kind == SpotterHit::Kind::Command);
  CHECK(heard->phrase == "стоп");
  CHECK_FALSE(hit(audio).has_value());

  // clear of both refractory windows, a wake and a command in one chunk come one per poll, the wake first
  pass(audio, kWakeRefractoryMs + 100);
  stock.hits = {{FakeComponent::Command, "тише"}, {FakeComponent::Alice, {}}};
  stock.events = {2, 2, 0};
  processSilence(audio);
  heard = hit(audio);
  REQUIRE(heard.has_value());
  CHECK(heard->kind == SpotterHit::Kind::Wake);
  heard = hit(audio);
  REQUIRE(heard.has_value());
  CHECK(heard->kind == SpotterHit::Kind::Command);
  CHECK(heard->phrase == "тише");
  CHECK_FALSE(hit(audio).has_value());
}

TEST_CASE_METHOD(StockFixture, "command hits wait out the same warm-up and refractory window as wakes") {
  StockAudio audio(loadFake);
  initialized(audio, commandConfig());
  stock.hits = {{FakeComponent::Command, "стоп"}};
  stock.events = {2, 0};
  processSilence(audio);
  CHECK_FALSE(hit(audio).has_value());

  warmUp(audio);
  // the second confirmation of one utterance is no second command
  stock.hits = {{FakeComponent::Command, "громче"}, {FakeComponent::Command, "громче"}};
  stock.events = {2, 5, 0};
  processSilence(audio);
  const auto heard = hit(audio);
  REQUIRE(heard.has_value());
  CHECK(heard->phrase == "громче");
  CHECK_FALSE(hit(audio).has_value());
  CHECK(stock.phraseCalls == 1);

  initialized(audio, commandConfig());
  stock.hits = {{FakeComponent::Command, "стоп"}};
  stock.events = {2, 0};
  processSilence(audio);
  CHECK_FALSE(hit(audio).has_value());
}

TEST_CASE_METHOD(StockFixture, "unions channel counts by maximum") {
  setOutputs(kOmni, -0.25f);
  setOutputs(kBnr, 0.375f);
  setOutputs(kRawMainMic, 0.125f);
  SECTION("commands need fewer channels than alice") {
    stock.spotterChannels = kAlisaChannels;
    stock.commandChannels = {1, 0, 0, 0, 0, 0};
  }
  SECTION("each conf adds channels the other lacks") {
    stock.spotterChannels = {1, 0, 0, 0, 0, 0};
    stock.commandChannels = {0, 0, 0, 1, 1, 0};
  }
  StockAudio audio(loadFake);

  const auto info = initialized(audio, commandConfig());
  processSilence(audio);

  REQUIRE(info.spotter.has_value());
  CHECK(info.spotter->commands);
  CHECK(info.spotter->channelTypes == std::vector<int>{0, 3, 4});
  CHECK(stock.multiCreateTypes == std::vector<int>{0, 3, 4});
  CHECK(info.outputTypes == std::vector<int>{6, 9, 8, 2});
  REQUIRE(stock.submitted.size() == static_cast<std::size_t>(stock.outputFrames) * 3);
  CHECK(std::vector<std::int16_t>(stock.submitted.begin(), stock.submitted.begin() + 3) ==
        std::vector<std::int16_t>{-8192, 12288, 4096});
}

TEST_CASE_METHOD(StockFixture, "command words stay off and alice listens alone when anything is missing") {
  stock.spotterChannels = kAlisaChannels;
  auto config = commandConfig();
  StockErrorCode code{};
  std::string detail;
  SECTION("a multi symbol is missing") {
    const std::string missing = GENERATE(from_range(std::begin(kMultiSpotterSymbols), std::end(kMultiSpotterSymbols)));
    stock.missingSymbols.insert(missing);
    code = StockErrorCode::MissingSymbol;
    detail = missing;
  }
  SECTION("no command model is given") {
    config.paths.commandModel.reset();
    code = StockErrorCode::SpotterConfigFailed;
    detail = "no command model";
  }
  SECTION("the command conf fails to read") {
    stock.failures["command-conf-read"] = "bad flags";
    code = StockErrorCode::SpotterConfigFailed;
    detail = "command model: bad flags";
  }
  SECTION("the command conf is not 16 kHz") {
    stock.commandNativeRate = 8000;
    code = StockErrorCode::SpotterConfigFailed;
    detail = "command model rate 8000";
  }
  SECTION("the alice conf is not 16 kHz") {
    stock.spotterNativeRate = 22050;
    code = StockErrorCode::SpotterConfigFailed;
    detail = "alice model rate 22050";
  }
  SECTION("vqe output is not 16 kHz") {
    stock.outputRate = 8000;
    stock.outputFrames = 80;
    for (const int type : {kOmni, kOmniWithGain, kRawMainMic, kBnr})
      setOutputs(type, 0.0f);
    config.outputSampleRate = 8000;
    code = StockErrorCode::ConfigMismatch;
    detail = "output rate 8000";
  }
  SECTION("the union layout is refused") {
    stock.commandChannels = {1, 0, 0, -1, 0, 0};
    code = StockErrorCode::SpotterLayoutUnsupported;
    detail = "channel type 3 x-1";
  }
  StockAudio audio(loadFake);

  const auto info = initialized(audio, config);

  REQUIRE(info.commandError.has_value());
  CHECK(info.commandError->code == code);
  CHECK_THAT(describe(*info.commandError), ContainsSubstring(detail));
  REQUIRE(info.spotter.has_value());
  CHECK_FALSE(info.spotter->commands);
  CHECK(stock.spotterCreateTypes == std::vector<int>{0, 3, 4});
  CHECK(stock.multiCalls.empty());
  CHECK(stock.allConfsReleased());
  CHECK(stock.everyIssuedFreedOnce());

  warmUp(audio);
  stock.events = {2, 0};
  processSilence(audio);
  CHECK(woke(audio));
  CHECK(stock.componentCalls == 0);
  CHECK(stock.phraseCalls == 0);
}

TEST_CASE_METHOD(StockFixture, "a library refusal while building the combined spotter tears it down and keeps alice") {
  std::size_t refused = 0;
  SECTION("multi conf") { stock.failures["multi-conf-create"] = "refused"; }
  SECTION("alice attach") {
    stock.failures["attach-alice"] = "refused";
    refused = 1;
  }
  SECTION("command attach") {
    stock.failures["attach-command"] = "refused";
    refused = 1;
  }
  SECTION("spotter") { stock.failures["multi-spotter-create"] = "refused"; }
  StockAudio audio(loadFake);

  const auto info = initialized(audio, commandConfig());

  REQUIRE(info.commandError.has_value());
  CHECK(info.commandError->code == StockErrorCode::SpotterCreateFailed);
  REQUIRE(info.spotter.has_value());
  CHECK_FALSE(info.spotter->commands);
  // the fake flags a teardown in the wrong order; nothing of the multi conf may be left
  CHECK_FALSE(stock.multiConfAlive);
  CHECK_FALSE(stock.multiSpotterAlive);
  // a conf attach refused is the library's all the same, so cadenced never frees it
  CHECK(stock.confsWith(FakeConf::State::Kept) == refused);
  CHECK(stock.confsWith(FakeConf::State::Freed) == stock.confs.size() - refused);
  CHECK(stock.everyIssuedFreedOnce());
  warmUp(audio);
  stock.events = {2, 0};
  processSilence(audio);
  CHECK(woke(audio));
  CHECK(stock.submittedTo == &stock.spotterHandle);
}

TEST_CASE_METHOD(StockFixture, "never asks for phrase 0") {
  StockAudio audio(loadFake);
  initialized(audio, commandConfig());
  // a command heard in the warm-up is dropped before its phrase is read
  stock.hits = {{FakeComponent::Command, "стоп"}};
  stock.events = {1, 2, 3, 0};
  processSilence(audio);
  warmUp(audio);

  // subthreshold, ignored, and terminal events carry no phrase, and alice hits are never asked for one
  stock.hits = {{FakeComponent::Alice, {}}};
  stock.events = {1, 3, 6, 2, 4};
  processSilence(audio);
  stock.events = {7, 9, 0};
  processSilence(audio);

  CHECK(woke(audio));
  CHECK(stock.componentCalls == 2);
  CHECK(stock.phraseCalls == 0);
  CHECK(stock.violations == std::vector<std::string>{});
}
