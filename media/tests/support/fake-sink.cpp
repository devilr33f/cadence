#include "support/fake-sink.h"

#include <algorithm>
#include <utility>

namespace cadence::fake_sink {

namespace {

// nothing real to wait for; a short wait keeps the playback thread quick to notice changes
constexpr auto kRoomWait = std::chrono::milliseconds(2);

}  // namespace

std::vector<std::int16_t> FakePcmSink::play(std::size_t frames) {
  std::vector<std::int16_t> heard;
  {
    const std::lock_guard lock(mutex_);
    const auto samples = std::min(frames * channels_, buffer_.size());
    heard.assign(buffer_.begin(), buffer_.begin() + static_cast<std::ptrdiff_t>(samples));
    buffer_.erase(buffer_.begin(), buffer_.begin() + static_cast<std::ptrdiff_t>(samples));
  }
  room_.notify_all();
  return heard;
}

void FakePcmSink::pushUnderrun() {
  const std::lock_guard lock(mutex_);
  faults_.push_back(PlaybackWrite::Kind::Recovered);
}

void FakePcmSink::pushFailure() {
  const std::lock_guard lock(mutex_);
  faults_.push_back(PlaybackWrite::Kind::Failed);
}

std::vector<std::int16_t> FakePcmSink::buffered() const {
  const std::lock_guard lock(mutex_);
  return {buffer_.begin(), buffer_.end()};
}

std::size_t FakePcmSink::bufferedFrames() const {
  const std::lock_guard lock(mutex_);
  return buffer_.size() / channels_;
}

std::vector<std::int16_t> FakePcmSink::written() const {
  const std::lock_guard lock(mutex_);
  return written_;
}

std::size_t FakePcmSink::peakFrames() const {
  const std::lock_guard lock(mutex_);
  return peakFrames_;
}

std::string FakePcmSink::calls() const {
  const std::lock_guard lock(mutex_);
  return calls_;
}

std::size_t FakePcmSink::rewoundFrames() const {
  const std::lock_guard lock(mutex_);
  return rewoundFrames_;
}

Result<PlaybackFormat, PlaybackError> FakePcmSink::open(const PlaybackFormat& format) {
  ++opens;
  requested = format;
  if (openError)
    return *openError;
  auto granted = grant.value_or(format);
  granted.fillFrames = format.fillFrames;
  const std::lock_guard lock(mutex_);
  channels_ = std::max<std::size_t>(granted.channels, 1);
  periodFrames_ = granted.periodFrames;
  capacityFrames_ = granted.fillFrames > 0 ? granted.fillFrames : granted.bufferFrames;
  return granted;
}

void FakePcmSink::blockWrites() {
  const std::lock_guard lock(mutex_);
  blockWrites_ = true;
}

void FakePcmSink::releaseWrites() {
  {
    const std::lock_guard lock(mutex_);
    blockWrites_ = false;
  }
  room_.notify_all();
}

bool FakePcmSink::writeBlocked() const {
  const std::lock_guard lock(mutex_);
  return writeWaiting_;
}

PlaybackWrite FakePcmSink::write(std::span<const std::int16_t> interleaved, std::chrono::milliseconds timeout) {
  std::unique_lock lock(mutex_);
  if (blockWrites_) {
    writeWaiting_ = true;
    room_.wait(lock, [&] { return !blockWrites_; });
    writeWaiting_ = false;
  }
  if (!faults_.empty()) {
    const auto fault = faults_.front();
    faults_.pop_front();
    if (fault == PlaybackWrite::Kind::Recovered) {
      buffer_.clear();
      return {PlaybackWrite::Kind::Recovered, 0, PlaybackError{PlaybackErrorCode::Underrun, "fake underrun"}};
    }
    return {PlaybackWrite::Kind::Failed, 0, PlaybackError{PlaybackErrorCode::WriteFailed, "fake write failure"}};
  }

  const auto room = [this] {
    const auto frames = buffer_.size() / channels_;
    return capacityFrames_ > frames ? capacityFrames_ - frames : 0;
  };
  room_.wait_for(lock, std::min(timeout, kRoomWait), [&] { return room() > 0; });
  const auto frames = std::min(room(), interleaved.size() / channels_);
  if (frames == 0)
    return {};
  const auto samples = interleaved.first(frames * channels_);
  buffer_.insert(buffer_.end(), samples.begin(), samples.end());
  written_.insert(written_.end(), samples.begin(), samples.end());
  peakFrames_ = std::max(peakFrames_, buffer_.size() / channels_);
  return {PlaybackWrite::Kind::Frames, frames, std::nullopt};
}

Result<std::size_t, PlaybackError> FakePcmSink::rewind() {
  std::size_t rewound = 0;
  {
    const std::lock_guard lock(mutex_);
    calls_.push_back('r');
    const auto frames = buffer_.size() / channels_;
    const auto kept = std::min(frames, periodFrames_);
    rewound = frames - kept;
    buffer_.resize(kept * channels_);
    rewoundFrames_ += rewound;
  }
  room_.notify_all();
  return rewound;
}

void FakePcmSink::failDelay(std::optional<PlaybackError> error) {
  const std::lock_guard lock(mutex_);
  delayError_ = std::move(error);
}

void FakePcmSink::setDelay(std::optional<std::size_t> frames) {
  const std::lock_guard lock(mutex_);
  delayOverride_ = frames;
}

Result<std::size_t, PlaybackError> FakePcmSink::delayFrames() {
  const std::lock_guard lock(mutex_);
  if (delayError_)
    return *delayError_;
  // the fake device has played exactly what play() took out; everything buffered is still unplayed
  return delayOverride_.value_or(buffer_.size() / channels_);
}

std::optional<PlaybackError> FakePcmSink::drop() {
  const std::lock_guard lock(mutex_);
  calls_.push_back('d');
  return std::nullopt;
}

void FakePcmSink::close() {
  ++closes;
  const std::lock_guard lock(mutex_);
  calls_.push_back('c');
}

std::vector<std::int16_t> channel(const std::vector<std::int16_t>& interleaved, std::size_t index) {
  std::vector<std::int16_t> samples;
  for (std::size_t offset = index; offset < interleaved.size(); offset += 2)
    samples.push_back(interleaved[offset]);
  return samples;
}

}  // namespace cadence::fake_sink
