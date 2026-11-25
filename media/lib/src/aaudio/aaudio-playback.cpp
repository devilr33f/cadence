#include "aaudio/aaudio-playback.h"

#include <algorithm>
#include <string>
#include <thread>

namespace cadence {

namespace {

// how often a write waiting for room looks again: aaudio's callback takes a burst (tens of ms) at a time
constexpr std::chrono::milliseconds kPoll(2);

}  // namespace

AAudioPlayback::AAudioPlayback(aaudio::Api& api) : api_(api) {}

AAudioPlayback::~AAudioPlayback() { close(); }

Result<PlaybackFormat, PlaybackError> AAudioPlayback::open(const PlaybackFormat& requested) {
  if (open_)
    return PlaybackError{PlaybackErrorCode::OpenFailed, "aaudio already open"};
  if (requested.sampleFormat != SampleFormat::S16Le || requested.channels == 0 || requested.periodFrames == 0)
    return PlaybackError{PlaybackErrorCode::ConfigureFailed, "aaudio plays s16 alone"};
  format_ = requested;
  if (auto error = openStream())
    return *error;
  open_ = true;
  return format_;
}

std::optional<PlaybackError> AAudioPlayback::openStream() {
  aaudio::StreamRequest request;
  request.direction = aaudio::Direction::Output;
  request.sampleRate = format_.sampleRate;
  request.channels = static_cast<int>(format_.channels);
  request.lowLatency = true;
  holding_.store(true);
  starved_.store(false);
  dead_.store(false);
  delivered_.store(0);
  lastCallbackNs_ = 0;
  auto opened = api_.open(
      request, [this](std::span<std::int16_t> out) { onData(out); },
      [this](int) { dead_.store(true, std::memory_order_release); });
  if (!opened.ok())
    return PlaybackError{PlaybackErrorCode::OpenFailed, "aaudio open: " + opened.error()};
  auto stream = std::move(opened.value());
  const auto info = stream->info();
  if (info.sampleRate != format_.sampleRate || info.channels != static_cast<int>(format_.channels))
    return PlaybackError{PlaybackErrorCode::FormatMismatch,
                         "aaudio granted " + std::to_string(info.sampleRate) + " Hz x " + std::to_string(info.channels)};
  burst_ = info.framesPerBurst > 0 ? info.framesPerBurst : static_cast<std::int32_t>(format_.periodFrames);
  const auto burst = static_cast<std::size_t>(burst_);
  fill_ = std::max(format_.fillFrames, kFillBursts * burst);
  const std::size_t capacity = std::max(format_.bufferFrames, fill_ + std::max(format_.periodFrames, burst));
  format_.fillFrames = fill_;
  format_.bufferFrames = capacity;
  // the ring before the callback can run
  ring_.reset(capacity, format_.channels);
  stream_ = std::move(stream);
  if (!stream_->start()) {
    stream_.reset();
    return PlaybackError{PlaybackErrorCode::OpenFailed, "aaudio start"};
  }
  return std::nullopt;
}

void AAudioPlayback::closeStream() {
  if (!stream_)
    return;
  stream_->stop();
  stream_.reset();
}

void AAudioPlayback::onData(std::span<std::int16_t> out) {
  const auto sequence = sequence_.load(std::memory_order_relaxed);
  sequence_.store(sequence + 1, std::memory_order_relaxed);
  std::atomic_thread_fence(std::memory_order_release);
  const std::size_t channels = ring_.channels();
  const std::size_t frames = out.size() / channels;
  const auto now = api_.nowNs();
  if (lastCallbackNs_ != 0)
    periods_.record(now - lastCallbackNs_);
  lastCallbackNs_ = now;
  if (const auto fill = ring_.fill(); fill < fillMin_.load(std::memory_order_relaxed))
    fillMin_.store(fill, std::memory_order_relaxed);
  if (ring_.takeCut())
    holding_.store(true, std::memory_order_relaxed);
  std::size_t n = 0;
  if (holding_.load(std::memory_order_relaxed)) {
    // silence, position held, until the ring holds its target again
    if (ring_.fill() >= fill_)
      holding_.store(false, std::memory_order_relaxed);
  }
  if (!holding_.load(std::memory_order_relaxed)) {
    n = ring_.read(out);
    if (n < frames) {
      underruns_.fetch_add(1, std::memory_order_relaxed);
      starved_.store(true, std::memory_order_release);
      holding_.store(true, std::memory_order_relaxed);
    }
  }
  std::fill(out.begin() + static_cast<std::ptrdiff_t>(n * channels), out.end(), std::int16_t{0});
  delivered_.store(delivered_.load(std::memory_order_relaxed) + static_cast<std::int64_t>(frames),
                   std::memory_order_relaxed);
  sequence_.store(sequence + 2, std::memory_order_release);
}

PlaybackWrite AAudioPlayback::write(std::span<const std::int16_t> interleaved, std::chrono::milliseconds timeout) {
  if (!open_)
    return {PlaybackWrite::Kind::Failed, 0, PlaybackError{PlaybackErrorCode::NotOpen, {}}};
  if (dead_.load(std::memory_order_acquire) || !stream_) {
    // a disconnect: the stream is closed here, never on aaudio's thread, and opened again on the default device
    closeStream();
    if (auto error = openStream())
      return {PlaybackWrite::Kind::Failed, 0, PlaybackError{PlaybackErrorCode::WriteFailed, error->detail}};
    ++reopens_;
    return {PlaybackWrite::Kind::Recovered, 0, PlaybackError{PlaybackErrorCode::Underrun, "aaudio stream reopened"}};
  }
  if (starved_.exchange(false, std::memory_order_acq_rel))
    return {PlaybackWrite::Kind::Recovered, 0, PlaybackError{PlaybackErrorCode::Underrun, "aaudio ring ran dry"}};
  const auto deadline = std::chrono::steady_clock::now() + timeout;
  for (;;) {
    const std::size_t fill = ring_.fill();
    const std::size_t room = std::min(fill_ > fill ? fill_ - fill : 0, ring_.room());
    if (room > 0) {
      const auto frames = std::min(room, interleaved.size() / format_.channels);
      const auto n = ring_.write(interleaved.first(frames * format_.channels));
      return {PlaybackWrite::Kind::Frames, n, std::nullopt};
    }
    const auto now = std::chrono::steady_clock::now();
    if (now >= deadline)
      return {PlaybackWrite::Kind::Idle, 0, std::nullopt};
    std::this_thread::sleep_for(std::min<std::chrono::steady_clock::duration>(kPoll, deadline - now));
  }
}

Result<std::size_t, PlaybackError> AAudioPlayback::rewind() {
  if (!open_)
    return PlaybackError{PlaybackErrorCode::NotOpen, {}};
  return ring_.cut();
}

Result<std::size_t, PlaybackError> AAudioPlayback::delayFrames() {
  if (!open_ || !stream_)
    return PlaybackError{PlaybackErrorCode::DelayFailed, "aaudio not open"};
  // the ring's fill and the frames handed over as one: a callback between the two reads would count its burst twice
  // or not at all
  std::size_t fill = 0;
  std::int64_t written = 0;
  for (int tries = 0; tries < 64; ++tries) {
    const auto before = sequence_.load(std::memory_order_acquire);
    fill = ring_.fill();
    written = delivered_.load(std::memory_order_relaxed);
    std::atomic_thread_fence(std::memory_order_acquire);
    if ((before & 1) == 0 && sequence_.load(std::memory_order_relaxed) == before)
      break;
    std::this_thread::yield();
  }
  std::int64_t held = 0;
  if (const auto ts = stream_->timestamp()) {
    // the frame heard at ts, moved on to now at the stream's rate; never past what was handed over
    const std::int64_t elapsed = std::max<std::int64_t>(0, api_.nowNs() - ts->monotonicNs);
    const std::int64_t presented =
        std::min<std::int64_t>(written, ts->framePosition + elapsed * format_.sampleRate / 1'000'000'000LL);
    held = written - presented;
    timestamped_ = true;
  } else {
    held = written - std::min(written, stream_->framesRead());
    timestamped_ = false;
  }
  return fill + static_cast<std::size_t>(std::max<std::int64_t>(0, held));
}

std::optional<PlaybackError> AAudioPlayback::drop() {
  if (!open_)
    return PlaybackError{PlaybackErrorCode::NotOpen, {}};
  ring_.cut();
  return std::nullopt;
}

void AAudioPlayback::close() {
  closeStream();
  open_ = false;
}

AAudioWindow AAudioPlayback::takeWindow() {
  AAudioWindow out;
  out.period = periods_.take();
  const auto least = fillMin_.exchange(SIZE_MAX, std::memory_order_relaxed);
  out.fillMinFrames = least == SIZE_MAX ? ring_.capacity() : least;
  return out;
}

AAudioPlaybackStatus AAudioPlayback::status() const {
  AAudioPlaybackStatus s;
  s.underruns = underruns_.load();
  s.reopens = reopens_;
  s.xruns = stream_ ? stream_->xruns() : 0;
  s.framesPerBurst = burst_;
  s.fillFrames = fill_;
  s.capacityFrames = ring_.capacity();
  s.timestamped = timestamped_;
  return s;
}

}  // namespace cadence
