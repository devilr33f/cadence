#include "backend/pcm-input.h"

#include <algorithm>
#include <chrono>
#include <thread>

namespace cadence {

namespace {

// a full ring is polled at this pace: a period of the playback thread frees 20 ms
constexpr auto kPoll = std::chrono::milliseconds(5);
// input frames converted at a time
constexpr std::size_t kChunkFrames = 1024;
// what one chunk can become at 48 kHz: from 8 kHz six times as many, and the resampler's slack
constexpr std::size_t kResampledFrames = kChunkFrames * (dsp::kRate / dsp::Resampler::kMinRate) + 64;
constexpr float kS16Scale = 1.0f / 32768.0f;

float toFloat(float sample) { return sample; }
float toFloat(std::int16_t sample) { return static_cast<float>(sample) * kS16Scale; }

// input frames at rate as 48 kHz frames, rounded
std::uint64_t at48k(std::size_t frames, std::uint32_t rate) {
  return (static_cast<std::uint64_t>(frames) * dsp::kRate + rate / 2) / rate;
}

}  // namespace

PcmInput::PcmInput()
    : ring_(kCapacityFrames * dsp::kStereo),
      converted_(kChunkFrames * dsp::kStereo),
      resampled_(kResampledFrames * dsp::kStereo),
      stereo_(kResampledFrames * dsp::kStereo) {}

bool PcmInput::push(std::span<const float> interleaved, std::uint32_t rate, std::size_t channels) {
  return enqueue(interleaved, rate, channels, true);
}

bool PcmInput::push(std::span<const std::int16_t> interleaved, std::uint32_t rate, std::size_t channels) {
  return enqueue(interleaved, rate, channels, true);
}

bool PcmInput::tryPush(std::span<const float> interleaved, std::uint32_t rate, std::size_t channels) {
  return enqueue(interleaved, rate, channels, false);
}

bool PcmInput::tryPush(std::span<const std::int16_t> interleaved, std::uint32_t rate, std::size_t channels) {
  return enqueue(interleaved, rate, channels, false);
}

void PcmInput::clear() { generation_.fetch_add(1, std::memory_order_acq_rel); }

void PcmInput::attach() { attached_.store(true, std::memory_order_relaxed); }

void PcmInput::detach() { attached_.store(false, std::memory_order_relaxed); }

PcmInputStatus PcmInput::status() const {
  std::uint64_t queued = 0;
  if (ackGeneration_.load(std::memory_order_acquire) == generation_.load(std::memory_order_acquire)) {
    const auto written = writePos_.load(std::memory_order_acquire);
    // until the consumer reads again it may still sit before the acknowledgement
    const auto read = std::max(readPos_.load(std::memory_order_acquire), ackPos_.load(std::memory_order_relaxed));
    queued = written > read ? written - read : 0;
  }
  return {.framesIn = framesIn_.load(std::memory_order_relaxed),
          .dropped = dropped_.load(std::memory_order_relaxed),
          .underruns = underruns_.load(std::memory_order_relaxed),
          .queued = queued,
          .framesRead = framesRead_.load(std::memory_order_relaxed),
          .writePos = writePos_.load(std::memory_order_acquire),
          .readPos = readPos_.load(std::memory_order_acquire)};
}

void PcmInput::acknowledge(std::uint64_t generation) {
  producerGeneration_ = generation;
  // the resampler's state is of the audio the clear dropped: the next push makes a fresh one, unsteered
  resampler_.reset();
  trimPpm_ = 0;
  rate_ = 0;
  channels_ = 0;
  ackPos_.store(writePos_.load(std::memory_order_relaxed), std::memory_order_relaxed);
  ackGeneration_.store(generation, std::memory_order_release);
}

bool PcmInput::writeConverted(std::span<const float> samples, std::size_t channels, bool wait) {
  if (channels == 1) {
    const std::size_t frames = samples.size();
    for (std::size_t i = 0; i < frames; ++i)
      stereo_[2 * i] = stereo_[2 * i + 1] = samples[i];
    samples = std::span<const float>(stereo_.data(), frames * dsp::kStereo);
  }
  return write(samples, wait);
}

bool PcmInput::flushResampler(bool wait) {
  bool whole = true;
  if (resampler_) {
    const std::span<float> out(resampled_.data(), kResampledFrames * channels_);
    for (std::size_t produced = 0; (produced = resampler_->flush(out)) > 0;)
      if (!writeConverted(out.first(produced * channels_), channels_, wait)) {
        whole = false;
        break;
      }
  }
  resampler_.reset();
  rate_ = 0;
  channels_ = 0;
  return whole;
}

void PcmInput::steer(std::int32_t ppm) {
  // a clear since the last push makes the next one start afresh: steering then is the new stream's
  if (const auto generation = generation_.load(std::memory_order_acquire); generation != producerGeneration_)
    acknowledge(generation);
  trimPpm_ = std::clamp(ppm, -dsp::Resampler::kMaxTrimPpm, dsp::Resampler::kMaxTrimPpm);
  if (resampler_)
    resampler_->setTrim(trimPpm_);
}

bool PcmInput::finish() {
  // after a clear the resampler holds audio of the dropped stream: acknowledging forgets it
  if (const auto generation = generation_.load(std::memory_order_acquire); generation != producerGeneration_) {
    acknowledge(generation);
    return true;
  }
  return flushResampler(true);
}

bool PcmInput::prepare(std::uint32_t rate, std::size_t channels, bool wait) {
  if (channels < 1 || channels > dsp::kStereo || rate < dsp::Resampler::kMinRate || rate > dsp::Resampler::kMaxRate)
    return false;
  const bool forced = rate == dsp::kRate && alwaysResample_.load(std::memory_order_relaxed);
  if (rate == rate_ && channels == channels_ && forced == forced_)
    return true;
  // a format change lets the old filter's tail out first, so a stream that changes rate has no gap
  flushResampler(wait);
  forced_ = forced;
  if (rate != dsp::kRate || forced) {
    auto created = dsp::Resampler::create(channels, rate);
    if (!created.ok())
      return false;
    resampler_.emplace(std::move(created.value()));
    resampler_->setTrim(trimPpm_);
  }
  rate_ = rate;
  channels_ = channels;
  return true;
}

template <typename Sample>
bool PcmInput::enqueue(std::span<const Sample> interleaved, std::uint32_t rate, std::size_t channels, bool wait) {
  // a push that starts after a clear is the new generation's
  if (const auto generation = generation_.load(std::memory_order_acquire); generation != producerGeneration_)
    acknowledge(generation);
  if (const auto ppm = requestedPpm_.exchange(kNoRequest, std::memory_order_relaxed); ppm != kNoRequest)
    steer(ppm);
  const auto pushing = producerGeneration_;
  // a clear while the old format's tail goes out stops this push too
  if (!prepare(rate, channels, wait) || producerGeneration_ != pushing)
    return false;

  const std::span<float> resampled(resampled_.data(), kResampledFrames * channels);
  const std::size_t frames = interleaved.size() / channels;
  // write dropped the rest of what it was given; when tryPush found no room, the input after consumed is dropped too.
  // a clear drops nothing: the rest is stale
  const auto stopped = [&](std::size_t consumed) {
    if (!wait && producerGeneration_ == pushing)
      dropped_.fetch_add(at48k(frames - consumed, rate), std::memory_order_relaxed);
    return false;
  };
  for (std::size_t first = 0; first < frames; first += kChunkFrames) {
    const std::size_t count = std::min(kChunkFrames, frames - first);
    const auto chunk = interleaved.subspan(first * channels, count * channels);
    std::transform(chunk.begin(), chunk.end(), converted_.begin(), [](Sample sample) { return toFloat(sample); });
    std::span<const float> pending(converted_.data(), count * channels);
    if (!resampler_) {
      if (!writeConverted(pending, channels, wait))
        return stopped(first + count);
      continue;
    }
    while (!pending.empty()) {
      const auto done = resampler_->process(pending, resampled);
      if (done.consumed == 0 && done.produced == 0)
        break;
      pending = pending.subspan(done.consumed * channels);
      if (!writeConverted(resampled.first(done.produced * channels), channels, wait))
        return stopped(first + count - pending.size() / channels);
    }
  }
  return true;
}

bool PcmInput::write(std::span<const float> stereo, bool wait) {
  const std::size_t frames = stereo.size() / dsp::kStereo;
  std::size_t done = 0;
  auto position = writePos_.load(std::memory_order_relaxed);
  while (done < frames) {
    // a clear makes the rest of this push stale
    if (const auto generation = generation_.load(std::memory_order_acquire); generation != producerGeneration_) {
      acknowledge(generation);
      return false;
    }
    // what lies before the acknowledgement is stale, whether or not the consumer read on to it: a consumer that
    // stopped reading (a group leader's idle feed) would otherwise leave no room after a clear of a full ring
    const auto free = std::max(readPos_.load(std::memory_order_acquire), ackPos_.load(std::memory_order_relaxed));
    const auto room = kCapacityFrames - static_cast<std::size_t>(position - free);
    if (room == 0) {
      if (!wait) {
        dropped_.fetch_add(frames - done, std::memory_order_relaxed);
        return false;
      }
      std::this_thread::sleep_for(kPoll);
      continue;
    }
    const std::size_t count = std::min(room, frames - done);
    for (std::size_t i = 0; i < count; ++i) {
      const std::size_t slot = static_cast<std::size_t>((position + i) % kCapacityFrames) * dsp::kStereo;
      ring_[slot] = stereo[(done + i) * dsp::kStereo];
      ring_[slot + 1] = stereo[(done + i) * dsp::kStereo + 1];
    }
    position += count;
    writePos_.store(position, std::memory_order_release);
    framesIn_.fetch_add(count, std::memory_order_relaxed);
    done += count;
  }
  return true;
}

std::size_t PcmInput::read(std::span<float> stereo) {
  const std::size_t frames = stereo.size() / dsp::kStereo;
  std::fill(stereo.begin(), stereo.end(), 0.0f);
  const auto generation = generation_.load(std::memory_order_acquire);
  auto position = readPos_.load(std::memory_order_relaxed);
  std::size_t count = 0;
  if (generation != consumerGeneration_) {
    if (ackGeneration_.load(std::memory_order_acquire) == generation) {
      // everything before the acknowledgement was queued before the clear
      position = std::max(position, ackPos_.load(std::memory_order_relaxed));
      readPos_.store(position, std::memory_order_release);
      consumerGeneration_ = generation;
    }
  }
  if (generation == consumerGeneration_) {
    count = std::min(frames, static_cast<std::size_t>(writePos_.load(std::memory_order_acquire) - position));
    for (std::size_t i = 0; i < count; ++i) {
      const std::size_t slot = static_cast<std::size_t>((position + i) % kCapacityFrames) * dsp::kStereo;
      stereo[i * dsp::kStereo] = ring_[slot];
      stereo[i * dsp::kStereo + 1] = ring_[slot + 1];
    }
    if (generation_.load(std::memory_order_acquire) != generation) {
      // cleared while copying: what was copied may be older than the clear, so none of it is handed out, and the
      // next read skips to the acknowledgement
      std::fill(stereo.begin(), stereo.end(), 0.0f);
      count = 0;
    } else {
      readPos_.store(position + count, std::memory_order_release);
      framesRead_.fetch_add(count, std::memory_order_relaxed);
    }
  }
  if (count < frames && attached_.load(std::memory_order_relaxed))
    underruns_.fetch_add(1, std::memory_order_relaxed);
  return count;
}

}  // namespace cadence
