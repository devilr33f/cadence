#include "capture/frame-buffer.h"

#include <algorithm>

namespace cadence {

std::optional<Pcm16Frame> PcmFramer::take(std::span<const std::int16_t>& input) {
  const auto count = std::min(input.size(), kPcm16FrameSamples - pending_);
  std::copy_n(input.data(), count, frame_.data() + pending_);
  input = input.subspan(count);
  pending_ += count;
  if (pending_ < kPcm16FrameSamples)
    return std::nullopt;
  pending_ = 0;
  return frame_;
}

CaptureFifo::CaptureFifo(std::size_t capacityFrames, std::size_t channels)
    : capacity_(capacityFrames), channels_(channels), ring_(capacityFrames * channels) {}

void CaptureFifo::copyFrames(std::uint64_t position, const std::int16_t* source, std::size_t frames) {
  for (std::size_t frame = 0; frame < frames; ++frame) {
    const auto slot = static_cast<std::size_t>((position + frame) % capacity_);
    std::copy_n(source + frame * channels_, channels_, ring_.data() + slot * channels_);
  }
}

std::size_t CaptureFifo::push(std::span<const std::int16_t> interleaved) {
  const std::size_t frames = interleaved.size() / channels_;
  if (frames == 0)
    return 0;
  std::size_t dropped = 0;
  {
    const std::lock_guard lock(mutex_);
    // frames beyond the capacity would be overwritten within this push anyway
    const std::size_t kept = std::min(frames, capacity_);
    const std::size_t skipped = frames - kept;
    copyFrames(writePosition_ + skipped, interleaved.data() + skipped * channels_, kept);
    writePosition_ += frames;
    if (writePosition_ - readPosition_ > capacity_) {
      const std::uint64_t oldest = writePosition_ - capacity_;
      dropped = static_cast<std::size_t>(oldest - readPosition_);
      readPosition_ = oldest;
      while (!gaps_.empty() && gaps_.front() <= oldest)
        gaps_.pop_front();
      gaps_.push_front(oldest);
    }
  }
  readable_.notify_one();
  return dropped;
}

void CaptureFifo::markGap() {
  const std::lock_guard lock(mutex_);
  if (gaps_.empty() || gaps_.back() != writePosition_)
    gaps_.push_back(writePosition_);
}

FifoRead CaptureFifo::pop(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) {
  const std::size_t room = interleaved.size() / channels_;
  std::unique_lock lock(mutex_);
  readable_.wait_for(lock, timeout, [this] { return closed_ || writePosition_ > readPosition_; });
  FifoRead read;
  if (room == 0 || writePosition_ == readPosition_)
    return read;
  if (!gaps_.empty() && gaps_.front() == readPosition_) {
    read.afterGap = true;
    gaps_.pop_front();
  }
  const std::uint64_t end = gaps_.empty() ? writePosition_ : gaps_.front();
  read.frames = static_cast<std::size_t>(std::min<std::uint64_t>(end - readPosition_, room));
  for (std::size_t frame = 0; frame < read.frames; ++frame) {
    const auto slot = static_cast<std::size_t>((readPosition_ + frame) % capacity_);
    std::copy_n(ring_.data() + slot * channels_, channels_, interleaved.data() + frame * channels_);
  }
  readPosition_ += read.frames;
  return read;
}

void CaptureFifo::discard() {
  const std::lock_guard lock(mutex_);
  readPosition_ = writePosition_;
  gaps_.assign(1, writePosition_);
}

void CaptureFifo::close() {
  {
    const std::lock_guard lock(mutex_);
    closed_ = true;
  }
  readable_.notify_all();
}

std::size_t CaptureFifo::size() const {
  const std::lock_guard lock(mutex_);
  return static_cast<std::size_t>(writePosition_ - readPosition_);
}

}  // namespace cadence
