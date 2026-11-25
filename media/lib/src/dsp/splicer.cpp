#include "dsp/splicer.h"

#include <algorithm>
#include <cmath>
#include <numbers>

namespace cadence::dsp {

namespace {

// a few seconds of packets: the scratch buffers never grow on the rtp thread once a stream runs
constexpr std::size_t kReserveFrames = 8192;

}  // namespace

Splicer::Splicer(std::size_t channels, std::size_t fadeFrames)
    : channels_(channels > 0 ? channels : 1), fade_(fadeFrames > 0 ? fadeFrames : 1), fadeIn_(fade_) {
  held_.reserve(fade_ * channels_);
  out_.reserve(kReserveFrames * channels_);
  joined_.reserve(kReserveFrames * channels_);
  cross_.reserve(kReserveFrames * channels_);
}

float Splicer::gain(std::size_t index) const {
  // raised cosine, 0 to 1 over fade_ frames, sampled at the middle of each frame
  const double x = (static_cast<double>(index) + 0.5) / static_cast<double>(fade_);
  return static_cast<float>(0.5 - 0.5 * std::cos(std::numbers::pi * std::min(x, 1.0)));
}

void Splicer::reset() {
  held_.clear();
  out_.clear();
  fadeIn_ = fade_;
  cutPending_ = false;
}

void Splicer::cut() {
  const std::size_t frames = held_.size() / channels_;
  for (std::size_t i = 0; i < frames; ++i) {
    // the held frames are the end of the old audio: the fade's last frames, wherever it starts
    const float g = gain(fade_ - frames + i);
    for (std::size_t c = 0; c < channels_; ++c)
      out_.push_back(held_[i * channels_ + c] * (1.0f - g));
  }
  held_.clear();
  fadeIn_ = fade_;
  cutPending_ = false;
}

void Splicer::emit(std::span<const float> interleaved) {
  const std::size_t frames = interleaved.size() / channels_;
  joined_.assign(held_.begin(), held_.end());
  for (std::size_t i = 0; i < frames; ++i) {
    float g = 1.0f;
    if (fadeIn_ > 0) {
      g = gain(fade_ - fadeIn_);
      --fadeIn_;
    }
    for (std::size_t c = 0; c < channels_; ++c)
      joined_.push_back(interleaved[i * channels_ + c] * g);
  }
  const std::size_t total = joined_.size() / channels_;
  const std::size_t keep = std::min(total, fade_);
  const auto split = joined_.begin() + static_cast<std::ptrdiff_t>((total - keep) * channels_);
  out_.insert(out_.end(), joined_.begin(), split);
  held_.assign(split, joined_.end());
}

void Splicer::append(std::span<const float> interleaved) {
  if (cutPending_)
    cut();
  emit(interleaved);
}

void Splicer::drop(std::span<const float> interleaved, std::size_t frames) {
  if (frames == 0) {
    append(interleaved);
    return;
  }
  const std::size_t total = interleaved.size() / channels_;
  if (frames >= total) {
    cutPending_ = true;
    return;
  }
  if (cutPending_ || fadeIn_ > 0 || total - frames < fade_ || frames < fade_) {
    // nothing whole to cross from: out of the old, into the kept
    cut();
    emit(interleaved.subspan(frames * channels_));
    return;
  }
  // the old audio's continuation (the dropped frames' start) crossfades into the first kept frames, then the rest
  cross_.clear();
  for (std::size_t i = 0; i < fade_; ++i) {
    const float g = gain(i);
    for (std::size_t c = 0; c < channels_; ++c)
      cross_.push_back(interleaved[i * channels_ + c] * (1.0f - g) + interleaved[(frames + i) * channels_ + c] * g);
  }
  cross_.insert(cross_.end(), interleaved.begin() + static_cast<std::ptrdiff_t>((frames + fade_) * channels_),
                interleaved.end());
  // the held frames go first, as they are: emit holds the last fade back again
  emit(cross_);
}

void Splicer::silence(std::size_t frames) {
  cut();
  out_.insert(out_.end(), frames * channels_, 0.0f);
}

}  // namespace cadence::dsp
