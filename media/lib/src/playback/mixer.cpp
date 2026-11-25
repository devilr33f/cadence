#include "playback/mixer.h"

#include <algorithm>
#include <cmath>
#include <limits>

namespace cadence {

namespace {

constexpr std::size_t indexOf(MixerSource source) { return static_cast<std::size_t>(source); }

std::int16_t clampToS16(std::int32_t value) {
  constexpr auto low = std::numeric_limits<std::int16_t>::min();
  constexpr auto high = std::numeric_limits<std::int16_t>::max();
  return static_cast<std::int16_t>(std::clamp<std::int32_t>(value, low, high));
}

// written so that nan also becomes silence
float clampGain(float gain) { return gain >= 0.0f ? std::min(gain, 1.0f) : 0.0f; }

}  // namespace

Mixer::Mixer(JitterBufferConfig voice) : slots_{Slot(voice), Slot(kToneQueue)} {}

JitterBuffer& Mixer::source(MixerSource source) { return slots_[indexOf(source)].buffer; }

const JitterBuffer& Mixer::source(MixerSource source) const { return slots_[indexOf(source)].buffer; }

void Mixer::setGain(MixerSource source, float gain) { slots_[indexOf(source)].gain = clampGain(gain); }

void Mixer::loop(std::span<const std::int16_t> period) {
  std::vector<std::int16_t> samples(period.begin(), period.end());
  std::shared_ptr<LoopFeed> feed;
  {
    const std::lock_guard lock(readMutex_);
    loop_.swap(samples);
    feed_.swap(feed);
    loopAt_ = 0;
  }
  // the old period is freed outside the lock, which the playback thread waits on
}

void Mixer::loopFeed(std::shared_ptr<LoopFeed> feed) {
  std::vector<std::int16_t> samples;
  const std::lock_guard lock(readMutex_);
  loop_.swap(samples);
  feed_.swap(feed);
  loopAt_ = 0;
}

bool Mixer::stopLoop() {
  std::vector<std::int16_t> stopped;
  std::shared_ptr<LoopFeed> feed;
  const std::lock_guard lock(readMutex_);
  if (loop_.empty() && !feed_)
    return false;
  loop_.swap(stopped);
  feed_.swap(feed);
  loopAt_ = 0;
  ++loopStops_;
  return true;
}

MixerRead Mixer::read(std::span<std::int16_t> mono) {
  const std::lock_guard lock(readMutex_);
  chunk_.resize(mono.size());
  sum_.assign(mono.size(), 0);

  MixerRead result;
  for (std::size_t index = 0; index < slots_.size(); ++index) {
    auto& slot = slots_[index];
    const auto read = slot.buffer.read(chunk_);
    const float gain = slot.gain.load();
    result.samples = std::max(result.samples, read.samples);
    result.sources[index] = read.samples;
    result.generation += read.generation;
    if (index == indexOf(MixerSource::Voice)) {
      result.underrun = read.underrun;
      result.drained = read.drained;
      result.marker = read.marker;
      result.voiceTrace = read.trace;
    }
    // past read.samples the source gave silence, which no gain changes
    for (std::size_t sample = 0; sample < read.samples; ++sample)
      sum_[sample] += static_cast<std::int32_t>(std::lround(static_cast<float>(chunk_[sample]) * gain));
  }
  if (!loop_.empty()) {
    const float gain = slots_[indexOf(MixerSource::Tone)].gain.load();
    for (auto& sample : sum_) {
      sample += static_cast<std::int32_t>(std::lround(static_cast<float>(loop_[loopAt_]) * gain));
      if (++loopAt_ == loop_.size())
        loopAt_ = 0;
    }
    result.samples = mono.size();
    result.sources[indexOf(MixerSource::Tone)] = mono.size();
  } else if (feed_) {
    const float gain = slots_[indexOf(MixerSource::Tone)].gain.load();
    feedChunk_.assign(mono.size(), 0);
    feed_->fill(feedChunk_);
    for (std::size_t sample = 0; sample < sum_.size(); ++sample)
      sum_[sample] += static_cast<std::int32_t>(std::lround(static_cast<float>(feedChunk_[sample]) * gain));
    // counted as tone samples, silent stretches too, so the music stays ducked under the whole ring
    result.samples = mono.size();
    result.sources[indexOf(MixerSource::Tone)] = mono.size();
  }
  result.generation += loopStops_.load();

  std::transform(sum_.begin(), sum_.end(), mono.begin(), clampToS16);
  return result;
}

std::uint64_t Mixer::generation() const {
  std::uint64_t sum = loopStops_.load();
  for (const auto& slot : slots_)
    sum += slot.buffer.generation();
  return sum;
}

}  // namespace cadence
