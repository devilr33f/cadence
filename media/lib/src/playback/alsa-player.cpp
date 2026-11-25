#include "playback/alsa-player.h"

#include "playback/voice-marks.h"

#include <algorithm>
#include <chrono>
#include <condition_variable>
#include <utility>
#include <vector>

namespace cadence {

namespace {

// bounds each wait for device room, so the playback thread notices cancels and stop within one period
constexpr std::chrono::milliseconds kWriteWait{50};
// a failing device is retried at this pace instead of in a tight loop
constexpr std::chrono::milliseconds kWriteRetryDelay{100};
// a cancel waits no longer than this for the playback thread, which only a stuck device delays
constexpr std::chrono::milliseconds kDiscardWait{250};
// what the player reads reaches the device this many frames later
constexpr std::uint64_t kLatency = PcmBackend::latencyFrames();

// when a period processed now is heard: after what the device holds and the backend's look-ahead; 0 when the
// device's delay is not known
std::int64_t heardAt(std::optional<std::uint64_t> unplayed) {
  if (!unplayed)
    return 0;
  const auto now = std::chrono::duration_cast<std::chrono::nanoseconds>(
                       std::chrono::steady_clock::now().time_since_epoch())
                       .count();
  return now + static_cast<std::int64_t>((*unplayed + kLatency) * 1'000'000'000ULL / static_cast<std::uint64_t>(kPlaybackSampleRate));
}

void fault(PlaybackStatus& status, const PlaybackError& error) {
  ++status.writeErrors;
  status.lastError = error;
}

}  // namespace

template <typename Update>
void AlsaPlayer::record(Update update) {
  const std::lock_guard lock(statusMutex_);
  update(status_);
}

AlsaPlayer::AlsaPlayer(PcmSink& sink, AlsaPlayerConfig config, PcmBackendInputs inputs)
    : sink_(sink), config_(config), mixer_(config.jitter), backend_(config.channels, inputs) {}

AlsaPlayer::~AlsaPlayer() { stop(); }

Result<PlaybackFormat, PlaybackError> AlsaPlayer::start() {
  if (thread_.joinable())
    return PlaybackError{PlaybackErrorCode::AlreadyRunning, {}};
  if (config_.channels != 1 && config_.channels != kPlaybackChannels)
    return PlaybackError{PlaybackErrorCode::FormatMismatch, "channels " + std::to_string(config_.channels)};

  const PlaybackFormat requested{kPlaybackSampleRate,  config_.channels,     SampleFormat::S16Le,
                                 config_.periodFrames, config_.bufferFrames, config_.maxDeviceFillFrames};
  const auto granted = sink_.open(requested);
  if (!granted.ok())
    return granted.error();
  if (auto mismatch = checkPlaybackFormat(requested, granted.value())) {
    sink_.close();
    return *mismatch;
  }

  format_ = granted.value();
  backend_.prepare(format_.periodFrames);
  {
    const std::lock_guard lock(discardMutex_);
    deviceGeneration_ = mixer_.generation() + discardBias_.load();
    threadRunning_ = true;
  }
  {
    const std::lock_guard lock(watchMutex_);
    running_ = true;
  }
  record([](PlaybackStatus& status) {
    status.running = true;
    // a stream that failed before a stop is fresh again
    status.failingSince.reset();
  });
  {
    const std::lock_guard lock(idleMutex_);
    idle_ = IdleSnapshot{};
    idle_.running = true;
  }
  thread_ = std::jthread([this](std::stop_token stop) { playLoop(stop); });
  return granted;
}

void AlsaPlayer::stop() {
  if (!thread_.joinable())
    return;
  thread_.request_stop();
  thread_.join();
  // the playback thread already settled any armed watch as failure on exit
  discardDevice();
  backend_.discard();
  sink_.close();
  mixer_.source(MixerSource::Voice).cancel();
  mixer_.source(MixerSource::Tone).cancel();
  mixer_.stopLoop();
  {
    const std::lock_guard lock(idleMutex_);
    idle_.running = false;
  }
  record([](PlaybackStatus& status) { status.running = false; });
}

bool AlsaPlayer::begin(std::string_view responseId) {
  const auto before = mixer_.generation();
  const bool started = mixer_.source(MixerSource::Voice).begin(responseId);
  if (started) {
    // close the idle window synchronously instead of waiting for the next playback-thread snapshot;
    // idle() also re-reads the queue live, so a snapshot publish can never overwrite this with stale empty
    const std::lock_guard lock(idleMutex_);
    idle_.queueEmpty = false;
  }
  // a response it replaced may still be in the device
  if (const auto after = mixer_.generation(); after != before)
    awaitDiscard(after + discardBias_.load());
  return started;
}

PushResult AlsaPlayer::push(std::string_view responseId, std::span<const std::int16_t> pcm) {
  return mixer_.source(MixerSource::Voice).push(responseId, pcm);
}

bool AlsaPlayer::end(std::string_view responseId) { return mixer_.source(MixerSource::Voice).end(responseId); }

void AlsaPlayer::cancel(std::optional<std::string_view> responseId) {
  if (mixer_.source(MixerSource::Voice).cancel(responseId)) {
    awaitDiscard(mixer_.generation() + discardBias_.load());
    return;
  }
  if (!responseId)
    return;
  // the response already left the queue (its audio is in the device, the jitter generation did not move):
  // cancelling exactly the watched key must still stop that audio and fail the watch, so the player's own
  // discard generation moves instead. any other retired or unknown key keeps the old no-op behavior
  bool forced = false;
  {
    const std::lock_guard lock(watchMutex_);
    if (watch_ && watch_->key == *responseId) {
      discardBias_.fetch_add(1);
      forced = true;
    }
  }
  if (forced)
    awaitDiscard(mixer_.generation() + discardBias_.load());
}

void AlsaPlayer::cancelFrom(std::string_view responseId) {
  if (mixer_.source(MixerSource::Voice).cancelFrom(responseId))
    awaitDiscard(mixer_.generation() + discardBias_.load());
}

PushResult AlsaPlayer::playTone(std::string_view key, std::span<const std::int16_t> pcm) {
  auto& tones = mixer_.source(MixerSource::Tone);
  if (!tones.begin(key))
    return PushResult::Stale;
  const auto pushed = tones.push(key, pcm);
  // ended even when refused, so the empty tone retires and never holds back the next
  tones.end(key);
  return pushed;
}

void AlsaPlayer::cancelTones() {
  const bool queued = mixer_.source(MixerSource::Tone).cancel();
  const bool looped = mixer_.stopLoop();
  if (queued || looped)
    awaitDiscard(mixer_.generation() + discardBias_.load());
}

void AlsaPlayer::loopTone(std::span<const std::int16_t> period) { mixer_.loop(period); }

void AlsaPlayer::loopFeed(std::shared_ptr<LoopFeed> feed) { mixer_.loopFeed(std::move(feed)); }

bool AlsaPlayer::stopLoop() {
  if (!mixer_.stopLoop())
    return false;
  awaitDiscard(mixer_.generation() + discardBias_.load());
  return true;
}

void AlsaPlayer::setMasterGain(float gain) { backend_.setVolume(gain); }

void AlsaPlayer::setGateOpen(bool open) {
  if (!backend_.setGateOpen(open) || open)
    return;
  // the music already in the device must not play on at a level that may not be the one asked for: the effective
  // generation moves, as for a watched-key cancel, so the playback thread takes the device back
  {
    const std::lock_guard lock(watchMutex_);
    discardBias_.fetch_add(1);
  }
  awaitDiscard(mixer_.generation() + discardBias_.load());
}

void AlsaPlayer::onCompletion(CompletionObserver observer) { observer_ = std::move(observer); }

void AlsaPlayer::onViz(VizObserver observer) { vizObserver_ = std::move(observer); }

void AlsaPlayer::traceTo(trace::Tracer tracer) { tracer_ = tracer; }

bool AlsaPlayer::watchCompletion(std::string_view responseId) {
  const std::lock_guard lock(watchMutex_);
  if (!running_ || watch_)
    return false;
  if (!mixer_.source(MixerSource::Voice).watch(responseId))
    return false;
  watch_ = Watch{std::string(responseId), mixer_.generation() + discardBias_.load(), std::nullopt};
  return true;
}

bool AlsaPlayer::idle() const {
  const std::lock_guard lock(idleMutex_);
  if (!idle_.running || !idle_.delayKnown || !idle_.queueEmpty || idle_.voicePending)
    return false;
  // the snapshot lags a begin() by up to one period; the queue itself is the authority for newly queued
  // voice, so no snapshot publish race can report idle while a response waits
  if (!mixer_.source(MixerSource::Voice).empty())
    return false;
  const auto played = idle_.written > idle_.delay ? idle_.written - idle_.delay : 0;
  return played >= idle_.voiceBoundary;
}

void AlsaPlayer::finishWatch(bool success) {
  std::string key;
  {
    const std::lock_guard lock(watchMutex_);
    if (!watch_)
      return;
    key = std::move(watch_->key);
    watch_.reset();
  }
  if (observer_)
    observer_(std::move(key), success);
}

PlaybackStatus AlsaPlayer::status() const {
  PlaybackStatus status;
  {
    const std::lock_guard lock(statusMutex_);
    status = status_;
  }
  status.queue = mixer_.source(MixerSource::Voice).status();
  return status;
}

void AlsaPlayer::discardDevice() {
  const auto rewound = sink_.rewind();
  const auto failed = sink_.drop();
  record([&rewound, &failed](PlaybackStatus& status) {
    ++status.discards;
    if (rewound.ok())
      status.rewoundFrames += rewound.value();
    else
      fault(status, rewound.error());
    if (failed)
      fault(status, *failed);
  });
}

void AlsaPlayer::awaitDiscard(std::uint64_t generation) {
  std::unique_lock lock(discardMutex_);
  discarded_.wait_for(lock, kDiscardWait, [&] { return !threadRunning_ || deviceGeneration_ >= generation; });
}

void AlsaPlayer::playLoop(std::stop_token stop) {
  // it must refill the device within one of its bursts whatever else the cpus run
  const auto priority = raiseThreadPriority(kNiceAudio);
  record([priority](PlaybackStatus& status) { status.priority = priority; });
  const auto period = format_.periodFrames;
  const auto channels = config_.channels;
  std::vector<std::int16_t> mono(period);
  std::vector<std::int16_t> out(period * channels);
  // frames read for the device but not yet written, and the queue generation they came from
  std::size_t pending = 0;
  std::size_t offset = 0;
  std::uint64_t pendingGeneration = 0;
  // the pending period's voice samples at its front
  std::size_t pendingVoice = 0;
  // the voice or the tones hold or sound in the pending period, which ducks the music
  bool pendingDucks = false;
  // the period was read but has not been through the backend yet
  bool unprocessed = false;
  // lifetime write watermarks: audible completion is judged against the device delay, never queue drain
  std::uint64_t writtenTotal = 0;
  // the absolute write position just past the last voice frame processed so far: the backend's look-ahead puts
  // it latency frames after where it was read, so it may still wait to be written
  std::uint64_t voiceEnd = 0;
  std::uint64_t deviceGeneration = 0;
  {
    const std::lock_guard lock(discardMutex_);
    deviceGeneration = deviceGeneration_;
  }
  std::mutex retryMutex;
  std::condition_variable_any retry;
  // every write since the last that took audio or recovered failed
  bool failing = false;
  // the device's delay at the last measurement, which says when the next period is heard
  std::optional<std::uint64_t> lastUnplayed;
  // the voice's trace marks, from the read of a response's first sample to the write that hands it to the device
  VoiceMarks marks(tracer_);

  while (!stop.stop_requested()) {
    // one bias reading per iteration keeps the pending period and the device on the same effective
    // generation; the completion settlement below re-reads it after the write
    const auto bias = discardBias_.load();
    if (pending == 0) {
      const auto read = mixer_.read(mono);
      pending = period;
      offset = 0;
      unprocessed = true;
      pendingGeneration = read.generation + bias;
      pendingVoice = read.samples;
      marks.read(read.voiceTrace, read.underrun, read.drained);
      pendingDucks = read.sources[static_cast<std::size_t>(MixerSource::Voice)] > 0 ||
                     read.sources[static_cast<std::size_t>(MixerSource::Tone)] > 0 ||
                     !mixer_.source(MixerSource::Voice).empty();
      if (read.marker) {
        // the watched response's last sample sits at this absolute write position: the period starts at
        // writtenTotal because the previous one was fully written, and the backend's look-ahead writes it
        // latency frames later. completion still has to wait for the delay watermark, and played <=
        // writtenTotal means its last frame must be written first. only the watch armed for this very
        // response takes the marker, so a stale marker can never complete a later watch
        const std::lock_guard lock(watchMutex_);
        if (watch_ && watch_->generation == read.generation + bias)
          watch_->marker = writtenTotal + static_cast<std::uint64_t>(*read.marker) + kLatency;
      }
    }

    if (const auto current = mixer_.generation() + bias; current != deviceGeneration) {
      // the device holds audio of a response that was cancelled or replaced, and the backend may still delay
      // some of it
      discardDevice();
      backend_.discard();
      voiceEnd = std::min(voiceEnd, writtenTotal);
      // what was not written of a begun response is gone with the backend's delay line
      marks.discarded();
      deviceGeneration = current;
      {
        const std::lock_guard lock(discardMutex_);
        deviceGeneration_ = current;
      }
      discarded_.notify_all();
    }
    // a cancel or replacement moved the queue past the watched response: its audio can no longer play out
    bool staleWatch = false;
    {
      const std::lock_guard lock(watchMutex_);
      staleWatch = watch_ && watch_->generation != deviceGeneration;
    }
    if (staleWatch)
      finishWatch(false);
    if (pendingGeneration != deviceGeneration) {
      pending = 0;
      pendingVoice = 0;
      continue;
    }
    // only once the period is known to be current, so a discarded one never reaches the backend's delay line
    if (unprocessed) {
      unprocessed = false;
      const auto frame = backend_.process(mono, pendingDucks, out, heardAt(lastUnplayed));
      if (pendingVoice > 0)
        voiceEnd = writtenTotal + pendingVoice + kLatency;
      marks.processed(writtenTotal, kLatency);
      if (frame && vizObserver_)
        vizObserver_(*frame);
    }

    const auto written = sink_.write(std::span(out).subspan(offset * channels, pending * channels), kWriteWait);
    if (written.kind == PlaybackWrite::Kind::Failed && !failing) {
      failing = true;
      record([now = std::chrono::steady_clock::now()](PlaybackStatus& status) { status.failingSince = now; });
    } else if ((written.kind == PlaybackWrite::Kind::Frames || written.kind == PlaybackWrite::Kind::Recovered) &&
               failing) {
      failing = false;
      record([](PlaybackStatus& status) { status.failingSince.reset(); });
    }
    switch (written.kind) {
      case PlaybackWrite::Kind::Idle:
        break;
      case PlaybackWrite::Kind::Frames: {
        const auto frames = std::min(written.frames, pending);
        offset += frames;
        pending -= frames;
        writtenTotal += frames;
        record([frames](PlaybackStatus& status) { status.frames += frames; });
        break;
      }
      // nothing was written, so the same audio is written again: it was never heard
      case PlaybackWrite::Kind::Recovered:
        record([](PlaybackStatus& status) { ++status.recoveries; });
        marks.recovered();
        // the stream restarted and what the device held is gone: no completion can be proven any more
        finishWatch(false);
        break;
      case PlaybackWrite::Kind::Failed: {
        record([&written](PlaybackStatus& status) { fault(status, *written.error); });
        finishWatch(false);
        std::unique_lock lock(retryMutex);
        retry.wait_for(lock, stop, kWriteRetryDelay, [] { return false; });
        break;
      }
    }

    // the device-delay watermark, queried only on this thread: played is clamped, never an unsigned
    // subtraction of a delay larger than everything submitted
    const auto delay = sink_.delayFrames();
    if (!delay.ok())
      finishWatch(false);
    const bool delayKnown = delay.ok();
    const std::uint64_t unplayed = delayKnown ? static_cast<std::uint64_t>(delay.value()) : 0;
    lastUnplayed = delayKnown ? std::optional<std::uint64_t>(unplayed) : std::nullopt;
    const std::uint64_t played = writtenTotal > unplayed ? writtenTotal - unplayed : 0;
    if (written.kind == PlaybackWrite::Kind::Frames)
      marks.wrote(writtenTotal, lastUnplayed, kPlaybackSampleRate);

    // settle against the generation re-read AFTER the write, inside the same critical section that clears
    // the watch: a cancel that landed while the write was blocked, or one that bumps the discard bias
    // while this settlement waits on the lock, must never be followed by a success. the decision and the
    // clear are linearized with the watched-key cancel's bias bump, which takes this lock too
    std::optional<std::pair<std::string, bool>> settled;
    {
      const std::lock_guard lock(watchMutex_);
      const auto settledGeneration = mixer_.generation() + discardBias_.load();
      // played <= writtenTotal, so reaching the marker also proves its last frame was written
      if (watch_ && watch_->generation != settledGeneration)
        settled = std::pair<std::string, bool>{std::move(watch_->key), false};
      else if (watch_ && watch_->marker && delayKnown && played >= *watch_->marker)
        settled = std::pair<std::string, bool>{std::move(watch_->key), true};
      if (settled)
        watch_.reset();
    }
    if (settled && observer_)
      observer_(std::move(settled->first), settled->second);

    {
      const std::lock_guard lock(idleMutex_);
      idle_.written = writtenTotal;
      idle_.voiceBoundary = voiceEnd;
      idle_.delay = unplayed;
      idle_.delayKnown = delayKnown;
      idle_.queueEmpty = mixer_.source(MixerSource::Voice).empty();
      idle_.voicePending = writtenTotal < voiceEnd;
      idle_.running = true;
    }
  }

  {
    // refuse new arms first, then settle anything armed: exactly one settlement per watch, a failure on
    // this exit even when the daemon stops mid-response
    const std::lock_guard lock(watchMutex_);
    running_ = false;
  }
  finishWatch(false);
  {
    const std::lock_guard lock(discardMutex_);
    threadRunning_ = false;
  }
  discarded_.notify_all();
}

}  // namespace cadence
