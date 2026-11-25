#include "pulse-shim/buffer.h"

#include <algorithm>
#include <utility>

namespace cadence::pulse {

namespace {

// a queue whose taken front grows past this is compacted
constexpr std::size_t kCompactAt = 64 * 1024;

}  // namespace

void ByteQueue::append(std::span<const std::uint8_t> bytes) {
  if (head_ >= kCompactAt || head_ == bytes_.size()) {
    bytes_.erase(bytes_.begin(), bytes_.begin() + static_cast<std::ptrdiff_t>(head_));
    head_ = 0;
  }
  bytes_.insert(bytes_.end(), bytes.begin(), bytes.end());
}

void ByteQueue::drop(std::size_t n) { head_ += std::min(n, size()); }

void ByteQueue::clear() {
  bytes_.clear();
  head_ = 0;
}

Buffer::Buffer(pcmframe::Format format, std::size_t tlength, std::size_t maxlength, bool corked)
    : tlength_(tlength), maxlength_(maxlength), corked_(corked) {
  control_.emplace_back(format);
  if (corked)
    control_.emplace_back(pcmframe::Cork{true});
}

std::size_t Buffer::room() const { return tlength_ > waiting_.size() ? tlength_ - waiting_.size() : 0; }

bool Buffer::write(std::span<const std::uint8_t> bytes) {
  if (waiting_.size() + bytes.size() > maxlength_)
    return false;
  waiting_.append(bytes);
  accepted_ += bytes.size();
  return true;
}

void Buffer::cork(bool on) {
  if (on == corked_)
    return;
  corked_ = on;
  control_.emplace_back(pcmframe::Cork{on});
  if (!on)
    starting_ = true;
}

void Buffer::flush() {
  const auto dropped = static_cast<std::uint64_t>(waiting_.size());
  waiting_.clear();
  control_.emplace_back(pcmframe::Flush{});
  const auto skipped = (flushes_.empty() ? skipped_ : flushes_.back().skipped) + dropped;
  flushes_.push_back({sent_, skipped});
  flowing_ = false;
  catchUp();
}

std::optional<Next> Buffer::next() {
  Next next;
  if (!control_.empty()) {
    const auto control = control_.front();
    control_.pop_front();
    std::visit([&](const auto& message) { pcmframe::encode(message, next.wire); }, control);
    next.format = std::holds_alternative<pcmframe::Format>(control);
    return next;
  }
  if (corked_ || waiting_.empty())
    return std::nullopt;
  // whole frames: every frame size the server takes divides kMaxPayload
  const auto n = std::min(waiting_.size(), pcmframe::kMaxPayload);
  next.wire.reserve(pcmframe::kHeaderBytes + n);
  pcmframe::encode(pcmframe::Pcm{waiting_.front(n)}, next.wire);
  waiting_.drop(n);
  next.pcm = n;
  sent_ += n;
  flowing_ = true;
  next.starts = std::exchange(starting_, false);
  if (next.starts)
    restarted_ = readIndex();
  return next;
}

bool Buffer::played(std::uint64_t played, timeval at) {
  // a report can only cover bytes that went out, and the read index never moves back
  played_ = std::max(played_, std::min(played, sent_));
  reported_ = at;
  catchUp();
  const bool dry = !corked_ && flowing_ && waiting_.empty() && readIndex() >= accepted_;
  if (dry) {
    flowing_ = false;
    starting_ = true;
  }
  return dry;
}

Indexes Buffer::indexes() const {
  return {.write = accepted_,
          .read = readIndex(),
          .playing = !corked_ && flowing_,
          .sinceUnderrun = readIndex() > restarted_ ? readIndex() - restarted_ : 0,
          .reported = reported_};
}

void Buffer::catchUp() {
  while (!flushes_.empty() && flushes_.front().sent <= played_) {
    skipped_ = flushes_.front().skipped;
    flushes_.pop_front();
  }
}

}  // namespace cadence::pulse
