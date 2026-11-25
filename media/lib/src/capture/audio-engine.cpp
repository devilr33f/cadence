#include "capture/audio-engine.h"

#include "text.h"

#include <algorithm>
#include <chrono>
#include <condition_variable>
#include <utility>

namespace cadence {

namespace {

constexpr std::size_t kFifoSeconds = 1;
// bound how long stop waits for each thread
constexpr std::chrono::milliseconds kCaptureWait{100};
constexpr std::chrono::milliseconds kProcessWait{100};
// a failing device is retried at this pace instead of in a tight loop
constexpr std::chrono::milliseconds kCaptureRetryDelay{100};

const char* summary(AudioEngineErrorCode code) {
  switch (code) {
    case AudioEngineErrorCode::AlreadyRunning:
      return "audio engine already running";
    case AudioEngineErrorCode::StockFailed:
      return "stock audio failed to start";
    case AudioEngineErrorCode::CaptureOpenFailed:
      return "capture failed to open";
    case AudioEngineErrorCode::CaptureFormatMismatch:
      return "capture format refused";
    case AudioEngineErrorCode::CaptureFailed:
      return "capture failed";
    case AudioEngineErrorCode::VqeFailed:
      return "vqe chunk failed";
    case AudioEngineErrorCode::SpotterFailed:
      return "wake spotter failed";
  }
  return "audio engine failed";
}

}  // namespace

std::string describe(const AudioEngineError& error) {
  std::string message = summary(error.code);
  if (!error.detail.empty())
    message.append(": ").append(error.detail);
  return text::boundedMessage(message.c_str());
}

template <typename Update>
void AudioEngine::record(Update update) {
  const std::lock_guard lock(statusMutex_);
  update(status_);
}

AudioEngine::AudioEngine(PcmSource& source, AudioEngineCallbacks callbacks, StockLibraryLoader loader)
    : source_(source), callbacks_(std::move(callbacks)), audio_(std::move(loader)) {}

AudioEngine::~AudioEngine() { stop(); }

Result<AudioEngineInfo, AudioEngineError> AudioEngine::start(const AudioEngineConfig& config) {
  if (processing_.joinable())
    return AudioEngineError{AudioEngineErrorCode::AlreadyRunning, {}};

  const auto stock = audio_.initialize(config.stock);
  if (!stock.ok())
    return AudioEngineError{AudioEngineErrorCode::StockFailed, describe(stock.error())};

  const auto& layout = config.stock.capture;
  const CaptureFormat requested{layout.sampleRate, layout.channels, layout.periodFrames, layout.bufferFrames};
  const auto granted = source_.open(requested);
  if (!granted.ok())
    return AudioEngineError{AudioEngineErrorCode::CaptureOpenFailed, describe(granted.error())};
  if (const auto mismatch = checkExactFormat(requested, granted.value())) {
    source_.close();
    return AudioEngineError{AudioEngineErrorCode::CaptureFormatMismatch, describe(*mismatch)};
  }

  info_ = AudioEngineInfo{stock.value(), granted.value()};
  readBuffer_.assign(std::max<std::size_t>(info_.capture.periodFrames, 1) * layout.channels, 0);
  chunk_.assign(info_.stock.chunkFrames * layout.channels, 0);
  assembledFrames_ = 0;
  rawChannels_.clear();
  for (const auto mic : layout.microphones)
    if (mic < layout.channels && rawChannels_.size() < kMaxRawChannels)
      rawChannels_.push_back(mic);
  if (rawChannels_.empty())
    rawChannels_.push_back(0);
  raw_.assign(info_.stock.chunkFrames * rawChannels_.size(), 0);
  // armed last, only once everything else has started, so a failed start never publishes anything
  playbackMonitoring_ = callbacks_.onPlaybackLevels && playbackMonitor_.configure(layout);
  {
    const std::lock_guard gate(gate_);
    // effective-mute transitions discard through it
    fifo_ = std::make_unique<CaptureFifo>(static_cast<std::size_t>(layout.sampleRate) * kFifoSeconds, layout.channels);
    streaming_ = false;
    framer_.clear();
    record([this](AudioEngineStatus& status) {
      status = AudioEngineStatus{};
      status.running = true;
      status.muted = muteState_.effectiveMuted;
      status.playbackMonitorAvailable = playbackMonitoring_;
      if (info_.stock.spotterError)
        status.lastError = AudioEngineError{AudioEngineErrorCode::SpotterFailed, describe(*info_.stock.spotterError)};
    });
  }

  processing_ = std::jthread([this](std::stop_token stop) { processLoop(stop); });
  capture_ = std::jthread([this](std::stop_token stop) { captureLoop(stop); });
  return info_;
}

void AudioEngine::stop() {
  if (!processing_.joinable())
    return;
  capture_.request_stop();
  processing_.request_stop();
  fifo_->close();
  capture_.join();
  processing_.join();
  source_.close();

  // only a running engine stops here, so this runs once per armed lifecycle: after capture has joined,
  // before any member of a still-alive owner (the led runner) is destroyed
  silencePlaybackLevels();

  const std::lock_guard gate(gate_);
  streaming_ = false;
  framer_.clear();
  record([](AudioEngineStatus& status) {
    status.running = false;
    status.streaming = false;
  });
}

void AudioEngine::setStreaming(bool enabled) {
  const std::lock_guard gate(gate_);
  if (enabled && muteState_.effectiveMuted)
    return;
  if (enabled != streaming_)
    framer_.clear();
  streaming_ = enabled;
  record([enabled](AudioEngineStatus& status) { status.streaming = enabled; });
}

void AudioEngine::setRawTap(RawTap tap) {
  const std::lock_guard gate(gate_);
  rawTap_ = std::move(tap);
}

MuteState AudioEngine::setPhysicalMuted(bool muted) { return setMuteSource(true, muted); }

MuteState AudioEngine::setPrivacyMuted(bool muted) { return setMuteSource(false, muted); }

MuteState AudioEngine::setMuteSource(bool physical, bool muted) {
  const std::lock_guard gate(gate_);
  bool& source = physical ? muteState_.physicalMuted : muteState_.privacyMuted;
  const bool effectiveBefore = muteState_.effectiveMuted;
  if (source != muted) {
    source = muted;
    muteState_.effectiveMuted = muteState_.physicalMuted || muteState_.privacyMuted;
    ++muteState_.generation;
  }
  if (muteState_.effectiveMuted != effectiveBefore) {
    const std::lock_guard capture(captureMutex_);
    ++muteEpoch_;
    // nothing captured before an effective mute may reach vqe after it, not even a partial chunk
    if (muteState_.effectiveMuted && fifo_)
      fifo_->discard();
    if (!muteState_.effectiveMuted)
      rearmWake_.store(true);
  }
  if (muteState_.effectiveMuted) {
    streaming_ = false;
    framer_.clear();
  }
  record([muted = muteState_.effectiveMuted](AudioEngineStatus& status) {
    status.muted = muted;
    status.streaming = status.streaming && !muted;
  });
  return muteState_;
}

MuteState AudioEngine::muteState() const {
  const std::lock_guard gate(gate_);
  return muteState_;
}

AudioEngineStatus AudioEngine::status() const {
  const std::lock_guard lock(statusMutex_);
  return status_;
}

void AudioEngine::publishPlaybackLevels(std::span<const std::int16_t> samples) {
  if (!playbackMonitoring_)
    return;
  if (const auto levels = playbackMonitor_.process(samples))
    callbacks_.onPlaybackLevels(*levels);
}

void AudioEngine::silencePlaybackLevels() {
  if (!playbackMonitoring_)
    return;
  playbackMonitor_.reset();
  callbacks_.onPlaybackLevels(MusicLevels{});
}

void AudioEngine::captureLoop(std::stop_token stop) {
  const auto channels = info_.capture.channels;
  std::mutex retryMutex;
  std::condition_variable_any retry;
  std::uint64_t seenEpoch = 0;
  {
    const std::lock_guard lock(captureMutex_);
    seenEpoch = muteEpoch_;
  }
  // after a muted read the device buffer may still hold audio captured while muted
  std::size_t silenceFrames = 0;
  // every read since the last that returned audio or recovered failed
  bool failing = false;
  while (!stop.stop_requested()) {
    const auto read = source_.read(readBuffer_, kCaptureWait);
    if (read.kind == CaptureRead::Kind::Failed && !failing) {
      failing = true;
      record([now = std::chrono::steady_clock::now()](AudioEngineStatus& status) { status.captureFailingSince = now; });
    } else if ((read.kind == CaptureRead::Kind::Frames || read.kind == CaptureRead::Kind::Recovered) && failing) {
      failing = false;
      record([](AudioEngineStatus& status) { status.captureFailingSince.reset(); });
    }
    switch (read.kind) {
      case CaptureRead::Kind::Idle:
        break;
      case CaptureRead::Kind::Frames: {
        const auto frames = std::min(read.frames, readBuffer_.size() / channels);
        const auto samples = std::span(readBuffer_).first(frames * channels);
        // the playback monitor reads the references before mute zeroing, outside captureMutex_: it measures
        // the digital reference even while the microphones are muted, and never sees the silenced copy
        publishPlaybackLevels(samples);
        std::size_t dropped = 0;
        {
          const std::lock_guard lock(captureMutex_);
          // muted now, or muted at some point since the last push: some of these frames may be muted audio
          const bool mutedSince = muteEpoch_ % 2 == 1 || muteEpoch_ != seenEpoch;
          seenEpoch = muteEpoch_;
          if (mutedSince) {
            std::fill(samples.begin(), samples.end(), std::int16_t{0});
            silenceFrames = info_.capture.bufferFrames;
          } else if (silenceFrames > 0) {
            const auto silenced = std::min(frames, silenceFrames);
            std::fill_n(samples.begin(), silenced * channels, std::int16_t{0});
            silenceFrames -= silenced;
          }
          dropped = fifo_->push(samples);
        }
        if (dropped > 0)
          record([dropped](AudioEngineStatus& status) { status.overflowFrames += dropped; });
        break;
      }
      case CaptureRead::Kind::Recovered:
        // the reference stream lost audio, so a partial window must not survive the gap
        silencePlaybackLevels();
        fifo_->markGap();
        record([](AudioEngineStatus& status) { ++status.recoveries; });
        break;
      case CaptureRead::Kind::Failed: {
        silencePlaybackLevels();
        fifo_->markGap();
        fault(AudioEngineErrorCode::CaptureFailed, read.error ? describe(*read.error) : std::string{},
              &AudioEngineStatus::captureErrors);
        std::unique_lock lock(retryMutex);
        retry.wait_for(lock, stop, kCaptureRetryDelay, [] { return false; });
        break;
      }
    }
  }
}

void AudioEngine::processLoop(std::stop_token stop) {
  const auto channels = info_.capture.channels;
  while (!stop.stop_requested()) {
    const auto room = std::span(chunk_).subspan(assembledFrames_ * channels);
    const auto read = fifo_->pop(room, kProcessWait);
    if (read.frames == 0)
      continue;
    if (read.afterGap) {
      // completing the partial chunk would splice audio from both sides of the gap into one vqe input
      if (assembledFrames_ > 0) {
        const auto fresh = room.first(read.frames * channels);
        std::copy(fresh.begin(), fresh.end(), chunk_.begin());
      }
      const auto discarded = std::exchange(assembledFrames_, 0);
      record([discarded](AudioEngineStatus& status) {
        ++status.gaps;
        status.discardedFrames += discarded;
      });
    }
    assembledFrames_ += read.frames;
    if (assembledFrames_ == info_.stock.chunkFrames) {
      processChunk();
      assembledFrames_ = 0;
    }
  }
}

void AudioEngine::processChunk() {
  // the stock engines belong to this thread, so the unmute only flags it
  if (rearmWake_.exchange(false))
    audio_.rearmWakeGates();
  tapRaw();
  const auto processed = audio_.process(chunk_, info_.stock.chunkFrames);
  if (processed.ok())
    deliver(processed.value().omniWithGain);
  else
    fault(AudioEngineErrorCode::VqeFailed, describe(processed.error()), &AudioEngineStatus::vqeErrors);

  pollHits();
  const auto spotter = audio_.spotterStats();
  record([&spotter](AudioEngineStatus& status) {
    ++status.chunks;
    status.spotterEvents = spotter.events;
    status.unknownComponents = spotter.unknownComponents;
    status.spotterRebuilds = spotter.rebuilds;
    status.spotterRebuildFailures = spotter.rebuildFailures;
  });
}

void AudioEngine::tapRaw() {
  const std::lock_guard gate(gate_);
  const auto channels = info_.capture.channels;
  const auto mics = rawChannels_.size();
  if (!rawTap_ || muteState_.effectiveMuted || mics == 0 || rawChannels_.front() >= channels)
    return;
  const auto frames = raw_.size() / mics;
  for (std::size_t i = 0; i < frames; ++i)
    for (std::size_t c = 0; c < mics; ++c)
      raw_[i * mics + c] = chunk_[i * channels + rawChannels_[c]];
  rawTap_(raw_, mics);
}

void AudioEngine::deliver(std::span<const std::int16_t> samples) {
  const std::lock_guard gate(gate_);
  // checked again after every frame because a callback may stop the stream
  while (!samples.empty() && streaming_ && !muteState_.effectiveMuted) {
    const auto frame = framer_.take(samples);
    if (!frame)
      continue;
    record([](AudioEngineStatus& status) { ++status.frames; });
    if (callbacks_.onPcm16)
      callbacks_.onPcm16(*frame);
  }
}

void AudioEngine::pollHits() {
  // one chunk can leave a wake and a command pending, and each poll returns one
  while (true) {
    const auto hit = audio_.pollHit();
    if (!hit.ok()) {
      fault(AudioEngineErrorCode::SpotterFailed, describe(hit.error()), &AudioEngineStatus::spotterErrors);
      return;
    }
    if (!hit.value())
      return;
    if (hit.value()->kind == SpotterHit::Kind::Wake)
      activate();
    else
      command(hit.value()->phrase);
  }
}

void AudioEngine::activate() {
  const std::lock_guard gate(gate_);
  if (muteState_.effectiveMuted) {
    record([](AudioEngineStatus& status) { ++status.suppressedWakes; });
    return;
  }
  // framing restarts only when streaming turns on, so a new activation never carries samples from the
  // previous one and a wake while streaming loses none of the utterance
  if (!streaming_)
    framer_.clear();
  streaming_ = true;
  record([](AudioEngineStatus& status) {
    ++status.wakes;
    status.streaming = true;
  });
  if (callbacks_.onWake)
    callbacks_.onWake();
}

void AudioEngine::command(const std::string& phrase) {
  const std::lock_guard gate(gate_);
  if (muteState_.effectiveMuted) {
    record([](AudioEngineStatus& status) { ++status.suppressedCommandHits; });
    return;
  }
  record([](AudioEngineStatus& status) { ++status.commandHits; });
  if (callbacks_.onCommand)
    callbacks_.onCommand(phrase);
}

void AudioEngine::fault(AudioEngineErrorCode code, std::string detail, std::uint64_t AudioEngineStatus::*counter) {
  record([&](AudioEngineStatus& status) {
    ++(status.*counter);
    status.lastError = AudioEngineError{code, std::move(detail)};
  });
}

}  // namespace cadence
