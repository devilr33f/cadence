#include "support/fake-stock.h"
#include "support/test-layout.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>
#include <catch2/generators/catch_generators_range.hpp>
#include <catch2/matchers/catch_matchers_string.hpp>

#include <cstddef>
#include <cstdint>
#include <iterator>
#include <limits>
#include <string>
#include <vector>

using namespace cadence;
using namespace cadence::fake_stock;
using Catch::Matchers::ContainsSubstring;

namespace {

// the first, a middle, and the last symbol the binder resolves: one path binds them all
const char* const kVqeSymbols[] = {"YandexVqeC_Config_CreateFromPreset", "YandexVqeC_Process", "YandexVqeC_Error_Free"};

}  // namespace

TEST_CASE_METHOD(StockFixture, "names each missing vqe symbol") {
  const std::string missing = GENERATE(from_range(std::begin(kVqeSymbols), std::end(kVqeSymbols)));
  stock.missingSymbols.insert(missing);
  StockAudio audio(loadFake);

  const auto error = initializeError(audio);

  CHECK(error.code == StockErrorCode::MissingSymbol);
  CHECK(error.detail == missing);
  CHECK_THAT(describe(error), ContainsSubstring(missing));
  CHECK(stock.configCreates == 0);
}

TEST_CASE_METHOD(StockFixture, "reports library load failures") {
  StockAudio audio([](const std::filesystem::path&) -> Result<std::shared_ptr<StockLibrary>, StockError> {
    return StockError{StockErrorCode::LibraryLoadFailed, "cannot open shared object"};
  });

  const auto error = initializeError(audio);

  CHECK(error.code == StockErrorCode::LibraryLoadFailed);
  CHECK_THAT(describe(error), ContainsSubstring("cannot open shared object"));
  const auto input = silence();
  const auto processed = audio.process(input, static_cast<std::size_t>(stock.chunkFrames));
  REQUIRE_FALSE(processed.ok());
  CHECK(processed.error().code == StockErrorCode::NotInitialized);
}

TEST_CASE_METHOD(StockFixture, "initializes the board preset from the stock paths") {
  StockAudio audio(loadFake);

  const auto info = audio.initialize(testConfig());

  REQUIRE(info.ok());
  CHECK(stock.libraryPath == std::filesystem::path("/stock/libs/libvendor.so"));
  CHECK(stock.preset == kTestVqePreset);
  CHECK(stock.vqeModels == "/stock/vqe_models");
  CHECK(stock.spotterModel == "/stock/spotter_models/activation/main");
  CHECK(stock.spotterCreateRate == 16000);
  CHECK(stock.spotterCreateReserved == nullptr);
  CHECK_FALSE(stock.spotterCreateFlag);
  CHECK(info.value().outputTypes == std::vector<int>{6, 9});
  CHECK(info.value().inputSampleRate == 48000);
  CHECK(info.value().outputSampleRate == 16000);
  CHECK_FALSE(info.value().spotterError.has_value());
}

TEST_CASE_METHOD(StockFixture, "rejects stock configs that disagree with the capture layout") {
  SECTION("microphones") { stock.mics = static_cast<int>(kTestCaptureLayout.microphones.size()) - 1; }
  SECTION("speakers") { stock.speakers = static_cast<int>(kTestCaptureLayout.speakers.size()) - 1; }
  SECTION("input rate") { stock.inputRate = 44100; }
  SECTION("output rate") { stock.outputRate = 8000; }
  SECTION("empty chunk") { stock.chunkFrames = 0; }
  SECTION("empty frame") { stock.outputFrames = 0; }
  SECTION("chunk longer than a second") {
    stock.chunkFrames = 96000;
    stock.outputFrames = 32000;
  }
  SECTION("chunk and frame of different durations") { stock.outputFrames = 100; }
  StockAudio audio(loadFake);

  const auto error = initializeError(audio);

  CHECK(error.code == StockErrorCode::ConfigMismatch);
  CHECK(stock.configFrees == 1);
  CHECK(stock.vqeModels.empty());
}

TEST_CASE_METHOD(StockFixture, "frees vqe errors after copying their message") {
  SECTION("config") {
    stock.failures["config"] = "unknown preset";
    StockAudio audio(loadFake);
    const auto error = initializeError(audio);
    CHECK(error.code == StockErrorCode::VqeConfigFailed);
    CHECK_THAT(describe(error), ContainsSubstring("unknown preset"));
  }
  SECTION("create") {
    stock.failures["create"] = "model not found";
    StockAudio audio(loadFake);
    const auto error = initializeError(audio);
    CHECK(error.code == StockErrorCode::VqeCreateFailed);
    CHECK_THAT(describe(error), ContainsSubstring("model not found"));
    CHECK(stock.configFrees == 1);
  }
  SECTION("process, then the next chunk works") {
    StockAudio audio(loadFake);
    initialize(audio);
    stock.failures["process"] = "bad input";
    const auto input = silence();
    const auto result = audio.process(input, static_cast<std::size_t>(stock.chunkFrames));
    REQUIRE_FALSE(result.ok());
    CHECK(result.error().code == StockErrorCode::VqeProcessFailed);
    CHECK_THAT(describe(result.error()), ContainsSubstring("bad input"));
    stock.failures.clear();
    CHECK(audio.process(input, static_cast<std::size_t>(stock.chunkFrames)).ok());
  }
  SECTION("output") {
    StockAudio audio(loadFake);
    initialize(audio);
    stock.failures["output"] = "no channel";
    const auto input = silence();
    const auto result = audio.process(input, static_cast<std::size_t>(stock.chunkFrames));
    REQUIRE_FALSE(result.ok());
    CHECK(result.error().code == StockErrorCode::VqeOutputFailed);
    CHECK_THAT(describe(result.error()), ContainsSubstring("no channel"));
  }
  REQUIRE_FALSE(stock.errors.empty());
  CHECK(stock.everyIssuedFreedOnce());
}

TEST_CASE_METHOD(StockFixture, "bounds stock error messages") {
  stock.failures["config"] = std::string(5000, 'x') + "\n\x01tail";
  StockAudio audio(loadFake);

  const auto error = initializeError(audio);

  CHECK(describe(error).size() <= cadence::kMaxStockErrorLength);
  CHECK(describe(error).find('\n') == std::string::npos);
  CHECK(describe(error).find('\x01') == std::string::npos);
}

TEST_CASE_METHOD(StockFixture, "releases engines, configs, and spotters") {
  {
    StockAudio audio(loadFake);
    initialize(audio);
    initialize(audio);

    CHECK(stock.configFrees == 1);
    CHECK(stock.vqeFrees == 1);
    CHECK(stock.spotterDestroys == 1);
  }
  CHECK(stock.configFrees == 2);
  CHECK(stock.vqeFrees == 2);
  CHECK(stock.spotterDestroys == 2);
  CHECK(stock.spotterConfFrees == 2);
}

TEST_CASE_METHOD(StockFixture, "rejects input that is not exactly one vqe chunk") {
  StockAudio audio(loadFake);
  initialize(audio);
  const auto frames = static_cast<std::size_t>(stock.chunkFrames);
  std::vector<std::int16_t> input;
  std::size_t claimed = frames;

  SECTION("short chunk") {
    input.assign((frames - 1) * testConfig().capture.channels, 0);
    claimed = frames - 1;
  }
  SECTION("frame count that disagrees with the samples") { input.assign(frames * 3, 0); }

  const auto result = audio.process(input, claimed);
  REQUIRE_FALSE(result.ok());
  CHECK(result.error().code == StockErrorCode::InvalidInput);
  CHECK(stock.processCalls == 0);
}

TEST_CASE("converts float samples to clipped s16") {
  CHECK(cadence::toPcm16(0.5f) == 16384);
  CHECK(cadence::toPcm16(-0.5f) == -16384);
  CHECK(cadence::toPcm16(1.0f) == 32767);
  CHECK(cadence::toPcm16(-1.0f) == -32768);
  CHECK(cadence::toPcm16(std::numeric_limits<float>::infinity()) == 32767);
  CHECK(cadence::toPcm16(std::numeric_limits<float>::quiet_NaN()) == 0);
}

TEST_CASE_METHOD(StockFixture, "clips vqe output outside the unit range") {
  for (const int type : {kOmni, kOmniWithGain}) {
    stock.outputs[type][0] = 1.5f;
    stock.outputs[type][1] = -1.5f;
  }
  StockAudio audio(loadFake);
  initialize(audio);

  const auto output = processSilence(audio);

  CHECK(output.omniWithGain[0] == 32767);
  CHECK(output.omniWithGain[1] == -32768);
  CHECK(stock.submitted[0] == 32767);
  CHECK(stock.submitted[1] == -32768);
}

TEST_CASE_METHOD(StockFixture, "rejects non-finite vqe output") {
  const float bad = GENERATE(std::numeric_limits<float>::quiet_NaN(), std::numeric_limits<float>::infinity());
  const int type = GENERATE(kOmni, kOmniWithGain, kBnr, kRawMainMic);
  stock.spotterChannels = kAlisaChannels;
  stock.outputs[type][3] = bad;
  StockAudio audio(loadFake);
  initialize(audio);
  const auto input = silence();

  const auto result = audio.process(input, static_cast<std::size_t>(stock.chunkFrames));

  REQUIRE_FALSE(result.ok());
  CHECK(result.error().code == StockErrorCode::NonFiniteOutput);
  CHECK(stock.submitted.empty());
}

TEST_CASE_METHOD(StockFixture, "rejects vqe output of the wrong length") {
  stock.outputs[kOmni].pop_back();
  StockAudio audio(loadFake);
  initialize(audio);
  const auto input = silence();

  const auto result = audio.process(input, static_cast<std::size_t>(stock.chunkFrames));

  REQUIRE_FALSE(result.ok());
  CHECK(result.error().code == StockErrorCode::VqeOutputFailed);
}
