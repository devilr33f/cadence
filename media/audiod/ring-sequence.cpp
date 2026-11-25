#include "audiod/ring-sequence.h"

#include <algorithm>
#include <utility>

namespace cadence::audiod {

namespace {

constexpr std::string_view kStalled = "timeout";

std::size_t lengthOf(const RingPart& part) {
  if (part.stream)
    return 1;
  return part.pcm ? part.pcm->size() : part.silence;
}

// parts with nothing to play would loop without ever filling a sample
std::vector<RingPart> playable(std::vector<RingPart> parts) {
  std::erase_if(parts, [](const RingPart& part) { return lengthOf(part) == 0; });
  return parts;
}

}  // namespace

RingSequence::RingSequence(std::vector<RingPart> parts, bool loop, std::vector<RingPart> fallback,
                           FallbackHook onFallback)
    : parts_(playable(std::move(parts))),
      loop_(loop),
      fallback_(playable(std::move(fallback))),
      onFallback_(std::move(onFallback)),
      entered_(parts_.size(), false) {
  // a fallback never streams: it is what sounds when streaming does not
  std::erase_if(fallback_, [](const RingPart& part) { return part.stream != nullptr; });
  if (!parts_.empty())
    entered_[0] = true;
}

RingSequence::~RingSequence() { stopStreams(); }

void RingSequence::fill(std::span<std::int16_t> out) {
  std::size_t at = 0;
  while (at < out.size()) {
    if (done_ || parts_.empty())
      break;
    const auto& part = parts_[index_];
    const auto want = out.subspan(at);
    if (part.stream) {
      if (const auto code = part.stream->failure(); !code.empty()) {
        fallBack(code);
        continue;
      }
      const auto read = part.stream->read(want);
      at += read.samples;
      if (read.samples > 0)
        starved_ = 0;
      if (read.ended) {
        advance();
        continue;
      }
      if (at < out.size()) {
        starved_ += out.size() - at;
        if (starved_ >= kRingStallSamples)
          fallBack(kStalled);
        break;
      }
      continue;
    }
    const auto length = lengthOf(part);
    const auto take = std::min(want.size(), length - offset_);
    if (part.pcm)
      std::copy_n(part.pcm->begin() + static_cast<std::ptrdiff_t>(offset_), take, want.begin());
    else
      std::fill_n(want.begin(), take, std::int16_t{0});
    at += take;
    offset_ += take;
    if (offset_ == length)
      advance();
  }
  std::fill(out.begin() + static_cast<std::ptrdiff_t>(at), out.end(), std::int16_t{0});
}

void RingSequence::advance() {
  offset_ = 0;
  starved_ = 0;
  if (++index_ == parts_.size()) {
    if (!loop_) {
      done_ = true;
      return;
    }
    index_ = 0;
  }
  if (const auto& next = parts_[index_]; next.stream) {
    if (entered_[index_])
      next.stream->rewind();
  }
  entered_[index_] = true;
}

void RingSequence::fallBack(std::string_view code) {
  if (fellBack_)
    return;
  fellBack_ = true;
  stopStreams();
  // the streams stay held, stopped, until the sequence goes: the playback thread frees nothing it need not
  parts_.swap(fallback_);
  entered_.assign(parts_.size(), true);
  loop_ = true;
  done_ = false;
  index_ = 0;
  offset_ = 0;
  starved_ = 0;
  if (onFallback_)
    onFallback_(code);
}

void RingSequence::stopStreams() {
  for (auto* parts : {&parts_, &fallback_})
    for (const auto& part : *parts)
      if (part.stream)
        part.stream->stop();
}

}  // namespace cadence::audiod
