#pragma once

#include "audio/stock-error.h"
#include "audio/stock-library.h"
#include "audio/stock-paths.h"
#include "audio/wake-gate.h"
#include "audio-layout.h"
#include "result.h"

#include <array>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <optional>
#include <span>
#include <string>
#include <vector>

namespace cadence {

// the preset and the capture layout are the board's
struct StockAudioConfig {
  StockPaths paths;
  std::string preset;
  CaptureLayout capture;
  int outputSampleRate = 16000;
  // stream time after each initialize during which spotter confirmations are ignored
  std::uint64_t wakeWarmupMs = kWakeWarmupMs;
  // one spotter hears alice and the words of paths.commandModel; when anything it needs is missing or would
  // make the library quick_exit, alice listens alone and info.commandError says why
  bool commandWords = false;
};

struct SpotterInfo {
  int sampleRate = 0;
  std::vector<int> channelTypes;
  // the spotter also hears command words
  bool commands = false;
};

struct StockAudioInfo {
  std::size_t chunkFrames = 0;
  std::size_t outputFrames = 0;
  int inputSampleRate = 0;
  int outputSampleRate = 0;
  // vqe output channel types read for every chunk: 6 and 9, plus those the spotter layout needs
  std::vector<int> outputTypes;
  // exactly one is set: the spotter is optional so vqe facts survive a spotter failure
  std::optional<SpotterInfo> spotter;
  std::optional<StockError> spotterError;
  // why command words were asked for, yet alice listens alone
  std::optional<StockError> commandError;
};

struct SpotterHit {
  enum class Kind { Wake, Command } kind;
  // what the command component heard, empty for a wake. it is what the owner said: never log it
  std::string phrase;
};

// one vqe chunk of 16 kHz mono audio: omni is echo-cancelled, omniWithGain also has agc and feeds stt
struct VqeChunk {
  std::vector<std::int16_t> omni;
  std::vector<std::int16_t> omniWithGain;
};

// counters only; never audio
struct SpotterStats {
  std::uint64_t submits = 0;
  std::array<std::uint64_t, 10> events{};
  std::uint64_t unknownEvents = 0;
  // confirmations the combined spotter credited to neither alice nor the command words
  std::uint64_t unknownComponents = 0;
  // the alice-alone spotter rebuilt to free its score records, and rebuilds that kept the old one
  std::uint64_t rebuilds = 0;
  std::uint64_t rebuildFailures = 0;
};

// clips to s16; nan becomes silence
std::int16_t toPcm16(float sample);

// single-threaded owner of the stock vqe engine and the spotter: alice alone, or alice and command words
class StockAudio {
 public:
  explicit StockAudio(StockLibraryLoader loader = openStockLibrary);
  ~StockAudio();

  StockAudio(const StockAudio&) = delete;
  StockAudio& operator=(const StockAudio&) = delete;

  // releases any previous engines first. fails, leaving nothing initialized, only when the vqe engine
  // cannot start; a spotter failure is returned in info.spotterError and by every pollHit
  Result<StockAudioInfo, StockError> initialize(const StockAudioConfig& config);

  // takes exactly one vqe chunk of interleaved s16 capture and feeds the spotter
  Result<VqeChunk, StockError> process(std::span<const std::int16_t> interleaved, std::size_t frameCount);

  // each wake and each command once, one per poll and a wake first; both pass the warm-up after initialize
  // and one utterance's repeated confirmations count once. without a spotter, every poll reports why
  Result<std::optional<SpotterHit>, StockError> pollHit();

  // starts the wake and command warm-ups again from the current stream time
  void rearmWakeGates();

  [[nodiscard]] SpotterStats spotterStats() const;

  // last chunk of a vqe output read for stt or the spotter (zeros before the first chunk); empty if not read
  [[nodiscard]] std::span<const std::int16_t> lastOutput(int outputType) const;

 private:
  struct Session;

  StockLibraryLoader loader_;
  std::unique_ptr<Session> session_;
};

}  // namespace cadence
