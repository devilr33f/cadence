#include "audio/stock-api.h"

#include "audio/spotter-layout.h"
#include "audio/stock-combined-spotter.h"
#include "audio/stock-multi-conf.h"
#include "audio/stock-symbols.h"
#include "audio/stock-types.h"
#include "audio/wake-gate.h"

#include <algorithm>
#include <cmath>
#include <optional>
#include <string>
#include <utility>

namespace cadence {

namespace abi = stock_abi;

namespace {

// the spotter normally ends each drain with a terminal event; this caps a misbehaving one
constexpr int kMaxSpotterEventsPerChunk = 32;
// the alice-alone spotter (spotter_create_multichannel) keeps a 12-byte score record per frame that only
// spotter_destroy frees, ~10 MB an hour on midi; rebuilding it this often holds that to ~1.5 MB
constexpr std::uint64_t kSpotterRebuildSeconds = 600;
// a command phrase is a few words; the bound keeps an unterminated buffer from running away
constexpr std::size_t kMaxPhraseBytes = 256;

bool countIs(int value, std::size_t expected) { return value >= 0 && static_cast<std::size_t>(value) == expected; }

std::optional<StockError> mismatch(std::string detail) {
  return StockError{StockErrorCode::ConfigMismatch, std::move(detail)};
}

std::optional<StockError> checkCaptureLayout(const CaptureLayout& capture) {
  const auto outside = [&capture](std::size_t channel) { return channel >= capture.channels; };
  if (capture.sampleRate <= 0 || capture.microphones.empty() || capture.speakers.empty() ||
      std::any_of(capture.microphones.begin(), capture.microphones.end(), outside) ||
      std::any_of(capture.speakers.begin(), capture.speakers.end(), outside))
    return mismatch("capture layout is invalid");
  return std::nullopt;
}

std::string copyPhrase(const char* phrase) {
  if (phrase == nullptr)
    return {};
  std::size_t length = 0;
  while (length < kMaxPhraseBytes && phrase[length] != '\0')
    ++length;
  return std::string(phrase, length);
}

}  // namespace

std::int16_t toPcm16(float sample) {
  if (std::isnan(sample))
    return 0;
  const float scaled = std::clamp(sample * 32768.0f, -32768.0f, 32767.0f);
  return static_cast<std::int16_t>(std::lrint(scaled));
}

struct StockAudio::Session {
  // declared first so every engine handle is released before the library goes away
  std::shared_ptr<StockLibrary> library;
  StockSymbols symbols;
  ConfigHandle config{nullptr, {&symbols}};
  VqeHandle vqe{nullptr, {&symbols}};
  // declared before the spotter so it is released after it: stock maind destroys the spotter before it
  // touches the multi conf
  MultiConfHandle multiConf{symbols};
  SpotterHandle spotter{nullptr, {&symbols}};
  // the combined spotter's components, owned by multiConf; both null while alice listens alone
  abi::Component* aliceComponent = nullptr;
  abi::Component* commandComponent = nullptr;

  StockAudioInfo info;
  CaptureLayout capture;
  // vqe outputs read per chunk: VqeChunk's omni and omni with gain, then any others the spotter layout needs
  std::vector<abi::VqeOutput> outputTypes{abi::VqeOutput::Omni, abi::VqeOutput::OmniWithGain};
  std::vector<std::vector<std::int16_t>> outputs;
  // per spotter channel: index into outputTypes
  std::vector<std::size_t> spotterInputs;

  std::vector<float> mics;
  std::vector<float> speakers;
  std::vector<std::int16_t> spotterBuffer;

  WakeGate wake;
  WakeGate command;
  // the accepted command's phrase, copied out of the library's buffer until it is polled
  std::string commandPhrase;
  // why there is no spotter; reported by every pollHit
  std::optional<StockError> spotterUnavailable;
  SpotterStats stats;
  // the alice model the alice-alone spotter was built from, and input frames since it was built
  std::filesystem::path aliceModel;
  std::uint64_t framesSinceBuild = 0;

  Session() = default;
  Session(const Session&) = delete;
  Session& operator=(const Session&) = delete;

  std::optional<StockError> open(const StockLibraryLoader& loader, const StockAudioConfig& settings);
  std::optional<StockError> openVqe(const StockAudioConfig& settings);
  std::optional<StockError> openSpotter(const StockAudioConfig& settings);
  std::optional<StockError> openAliceSpotter(const StockAudioConfig& settings);
  void useLayout(const SpotterLayout& layout);

  Result<VqeChunk, StockError> process(std::span<const std::int16_t> interleaved, std::size_t frameCount);
  std::optional<StockError> readOutput(std::size_t index);
  void feedSpotter();
  void rebuildAliceSpotter();
  void drainSpotterEvents();
  void confirmHit();
  std::optional<SpotterHit> takeHit();
};

std::optional<StockError> StockAudio::Session::open(const StockLibraryLoader& loader, const StockAudioConfig& settings) {
  if (auto error = checkCaptureLayout(settings.capture))
    return error;
  capture = settings.capture;

  auto loaded = loader(settings.paths.library);
  if (!loaded.ok())
    return loaded.error();
  library = loaded.value();
  if (!library)
    return StockError{StockErrorCode::LibraryLoadFailed, "loader returned no library"};
  if (auto error = bindVqeSymbols(*library, symbols))
    return error;
  if (auto error = openVqe(settings))
    return error;

  mics.resize(info.chunkFrames * capture.microphones.size());
  speakers.resize(info.chunkFrames * capture.speakers.size());
  wake = WakeGate(info.inputSampleRate, settings.wakeWarmupMs);
  command = WakeGate(info.inputSampleRate, settings.wakeWarmupMs);

  auto spotterError = bindSpotterSymbols(*library, symbols);
  if (!spotterError)
    spotterError = openSpotter(settings);
  if (spotterError) {
    info.spotterError = spotterError;
    spotterUnavailable = std::move(spotterError);
  }
  spotterBuffer.resize(info.outputFrames * spotterInputs.size());
  outputs.assign(outputTypes.size(), std::vector<std::int16_t>(info.outputFrames));
  for (const auto type : outputTypes)
    info.outputTypes.push_back(static_cast<int>(type));
  return std::nullopt;
}

std::optional<StockError> StockAudio::Session::openVqe(const StockAudioConfig& settings) {
  abi::VqeConfig* rawConfig = nullptr;
  auto* configError = symbols.configCreate(settings.preset.c_str(), &rawConfig);
  config.reset(rawConfig);
  if (auto error = takeVqeError(symbols, configError, StockErrorCode::VqeConfigFailed))
    return error;
  if (!config)
    return StockError{StockErrorCode::VqeConfigFailed, "no config for preset"};

  const int chunk = symbols.chunkFrames(config.get());
  const int frame = symbols.outputFrames(config.get());
  const int inputRate = symbols.inputRate(config.get());
  const int outputRate = symbols.outputRate(config.get());
  if (inputRate != capture.sampleRate)
    return mismatch("input rate " + std::to_string(inputRate) + ", capture " + std::to_string(capture.sampleRate));
  if (outputRate != settings.outputSampleRate)
    return mismatch("output rate " + std::to_string(outputRate) + ", expected " +
                    std::to_string(settings.outputSampleRate));
  if (!countIs(symbols.micCount(config.get()), capture.microphones.size()))
    return mismatch("microphone count differs from capture");
  if (!countIs(symbols.speakerCount(config.get()), capture.speakers.size()))
    return mismatch("speaker count differs from capture");
  if (chunk <= 0 || chunk > inputRate || frame <= 0 || frame > outputRate)
    return mismatch("chunk " + std::to_string(chunk) + " frames, output " + std::to_string(frame) + " samples");
  if (static_cast<long long>(chunk) * outputRate != static_cast<long long>(frame) * inputRate)
    return mismatch("chunk and output durations differ");

  info.chunkFrames = static_cast<std::size_t>(chunk);
  info.outputFrames = static_cast<std::size_t>(frame);
  info.inputSampleRate = inputRate;
  info.outputSampleRate = outputRate;

  abi::Vqe* rawVqe = nullptr;
  auto* createError = symbols.vqeCreate(config.get(), settings.paths.vqeModels.c_str(), 0, nullptr, &rawVqe);
  vqe.reset(rawVqe);
  if (auto error = takeVqeError(symbols, createError, StockErrorCode::VqeCreateFailed))
    return error;
  if (!vqe)
    return StockError{StockErrorCode::VqeCreateFailed, "no engine"};
  return std::nullopt;
}

std::optional<StockError> StockAudio::Session::openSpotter(const StockAudioConfig& settings) {
  if (settings.commandWords) {
    const auto combined =
        openCombinedSpotter(*library, symbols, settings.paths, info.outputSampleRate, multiConf, spotter);
    if (combined.ok()) {
      aliceComponent = combined.value().alice;
      commandComponent = combined.value().commands;
      useLayout(combined.value().layout);
      info.spotter = SpotterInfo{kCombinedSpotterRate, combined.value().layout.channelTypes, true};
      return std::nullopt;
    }
    info.commandError = combined.error();
  }
  return openAliceSpotter(settings);
}

std::optional<StockError> StockAudio::Session::openAliceSpotter(const StockAudioConfig& settings) {
  SpotterConfHandle conf{nullptr, {&symbols}};
  if (auto error = readSpotterConf(symbols, settings.paths.activationModel, conf))
    return error;
  auto layout = spotterLayout(spotterChannelCounts(symbols, conf.get()));
  if (!layout.ok())
    return layout.error();
  const auto& channelTypes = layout.value().channelTypes;

  char* createMessage = nullptr;
  spotter.reset(symbols.spotterCreate(conf.get(), info.outputSampleRate, static_cast<int>(channelTypes.size()),
                                      channelTypes.data(), &createMessage, nullptr, false));
  auto createDetail = takeSpotterMessage(symbols, createMessage, "no spotter");
  if (!spotter)
    return StockError{StockErrorCode::SpotterCreateFailed, std::move(createDetail)};

  useLayout(layout.value());
  info.spotter = SpotterInfo{symbols.confNativeRate(conf.get()), channelTypes, false};
  aliceModel = settings.paths.activationModel;
  return std::nullopt;
}

void StockAudio::Session::useLayout(const SpotterLayout& layout) {
  for (const auto source : layout.sources) {
    auto read = std::find(outputTypes.begin(), outputTypes.end(), source);
    if (read == outputTypes.end())
      read = outputTypes.insert(read, source);
    spotterInputs.push_back(static_cast<std::size_t>(read - outputTypes.begin()));
  }
}

Result<VqeChunk, StockError> StockAudio::Session::process(std::span<const std::int16_t> interleaved,
                                                          std::size_t frameCount) {
  if (frameCount != info.chunkFrames || interleaved.size() != frameCount * capture.channels)
    return StockError{StockErrorCode::InvalidInput, "expected " + std::to_string(info.chunkFrames) + " frames of " +
                                                        std::to_string(capture.channels) + " channels"};
  wake.advance(frameCount);
  command.advance(frameCount);

  for (std::size_t frame = 0; frame < frameCount; ++frame) {
    const auto* samples = &interleaved[frame * capture.channels];
    for (std::size_t mic = 0; mic < capture.microphones.size(); ++mic)
      mics[frame * capture.microphones.size() + mic] = samples[capture.microphones[mic]] / 32768.0f;
    for (std::size_t speaker = 0; speaker < capture.speakers.size(); ++speaker)
      speakers[frame * capture.speakers.size() + speaker] = samples[capture.speakers[speaker]] / 32768.0f;
  }
  if (auto error = takeVqeError(symbols,
                                symbols.vqeProcess(vqe.get(), mics.data(), mics.size(), speakers.data(), speakers.size()),
                                StockErrorCode::VqeProcessFailed))
    return *error;

  for (std::size_t index = 0; index < outputTypes.size(); ++index) {
    if (auto error = readOutput(index))
      return *error;
  }
  if (spotter)
    feedSpotter();
  return VqeChunk{.omni = outputs[0], .omniWithGain = outputs[1]};
}

std::optional<StockError> StockAudio::Session::readOutput(std::size_t index) {
  const auto type = static_cast<int>(outputTypes[index]);
  const auto name = "output " + std::to_string(type);
  const float* data = nullptr;
  std::size_t samples = 0;
  if (auto error = takeVqeError(symbols, symbols.vqeOutput(vqe.get(), type, 0, &data, &samples),
                                StockErrorCode::VqeOutputFailed)) {
    error->detail.insert(0, name + ": ");
    return error;
  }
  if (data == nullptr || samples != info.outputFrames)
    return StockError{StockErrorCode::VqeOutputFailed, name + " has " + std::to_string(samples) + " samples, expected " +
                                                           std::to_string(info.outputFrames)};
  if (!std::all_of(data, data + samples, [](float sample) { return std::isfinite(sample); }))
    return StockError{StockErrorCode::NonFiniteOutput, name};
  std::transform(data, data + samples, outputs[index].begin(), toPcm16);
  return std::nullopt;
}

void StockAudio::Session::feedSpotter() {
  const std::size_t channels = spotterInputs.size();
  for (std::size_t frame = 0; frame < info.outputFrames; ++frame)
    for (std::size_t channel = 0; channel < channels; ++channel)
      spotterBuffer[frame * channels + channel] = outputs[spotterInputs[channel]][frame];

  symbols.spotterSubmit(spotter.get(), spotterBuffer.data(), spotterBuffer.size() * sizeof(std::int16_t));
  ++stats.submits;
  drainSpotterEvents();
  framesSinceBuild += info.chunkFrames;
  if (!aliceModel.empty() && framesSinceBuild >= kSpotterRebuildSeconds * static_cast<std::uint64_t>(info.inputSampleRate))
    rebuildAliceSpotter();
}

// the new spotter is built before the old one goes, so a failed build leaves alice listening. a wake word spoken
// across the swap can be missed, once per period
void StockAudio::Session::rebuildAliceSpotter() {
  framesSinceBuild = 0;
  SpotterConfHandle conf{nullptr, {&symbols}};
  if (readSpotterConf(symbols, aliceModel, conf)) {
    ++stats.rebuildFailures;
    return;
  }
  const auto& types = info.spotter->channelTypes;
  char* message = nullptr;
  SpotterHandle fresh{symbols.spotterCreate(conf.get(), info.outputSampleRate, static_cast<int>(types.size()),
                                            types.data(), &message, nullptr, false),
                      {&symbols}};
  takeSpotterMessage(symbols, message, {});
  if (!fresh) {
    ++stats.rebuildFailures;
    return;
  }
  spotter = std::move(fresh);
  ++stats.rebuilds;
}

void StockAudio::Session::drainSpotterEvents() {
  for (int drained = 0; drained < kMaxSpotterEventsPerChunk; ++drained) {
    const int event = symbols.spotterNextEvent(spotter.get());
    if (event < 0 || event > abi::kSpotterMaxEvent) {
      ++stats.unknownEvents;
      return;
    }
    ++stats.events[static_cast<std::size_t>(event)];
    if (abi::endsSpotterEvents(event))
      return;
    if (event == abi::kSpotterFirstConfirmation || event == abi::kSpotterSecondConfirmation)
      confirmHit();
  }
}

void StockAudio::Session::confirmHit() {
  if (commandComponent == nullptr) {
    wake.confirm();
    return;
  }
  const auto* component = symbols.currentComponent(spotter.get());
  if (component == aliceComponent) {
    wake.confirm();
  } else if (component == commandComponent) {
    // asked only for a command hit, never for phrase 0, and copied before any other call rewrites the buffer
    if (command.confirm())
      commandPhrase = copyPhrase(symbols.currentPhrase(spotter.get()));
  } else {
    ++stats.unknownComponents;
  }
}

std::optional<SpotterHit> StockAudio::Session::takeHit() {
  if (wake.take())
    return SpotterHit{SpotterHit::Kind::Wake, {}};
  if (command.take())
    return SpotterHit{SpotterHit::Kind::Command, std::exchange(commandPhrase, {})};
  return std::nullopt;
}

StockAudio::StockAudio(StockLibraryLoader loader) : loader_(std::move(loader)) {}

StockAudio::~StockAudio() = default;

Result<StockAudioInfo, StockError> StockAudio::initialize(const StockAudioConfig& config) {
  session_.reset();
  auto session = std::make_unique<Session>();
  if (auto error = session->open(loader_, config))
    return *error;
  session_ = std::move(session);
  return session_->info;
}

Result<VqeChunk, StockError> StockAudio::process(std::span<const std::int16_t> interleaved, std::size_t frameCount) {
  if (!session_)
    return StockError{StockErrorCode::NotInitialized, {}};
  return session_->process(interleaved, frameCount);
}

std::span<const std::int16_t> StockAudio::lastOutput(int outputType) const {
  if (!session_)
    return {};
  const auto& types = session_->outputTypes;
  const auto read = std::find(types.begin(), types.end(), static_cast<abi::VqeOutput>(outputType));
  if (read == types.end())
    return {};
  return session_->outputs[static_cast<std::size_t>(read - types.begin())];
}

void StockAudio::rearmWakeGates() {
  if (!session_)
    return;
  session_->wake.rearm();
  session_->command.rearm();
}

SpotterStats StockAudio::spotterStats() const { return session_ ? session_->stats : SpotterStats{}; }

Result<std::optional<SpotterHit>, StockError> StockAudio::pollHit() {
  if (!session_)
    return StockError{StockErrorCode::NotInitialized, {}};
  if (session_->spotterUnavailable)
    return *session_->spotterUnavailable;
  return session_->takeHit();
}

}  // namespace cadence
