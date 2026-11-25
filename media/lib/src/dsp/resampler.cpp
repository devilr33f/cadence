#include "dsp/resampler.h"


#include <speex/speex_resampler.h>

#include <algorithm>
#include <cmath>
#include <limits>
#include <utility>

namespace cadence::dsp {

namespace {

constexpr int kQuality = 8;
// the trimmed ratio's denominator. speexdsp rescales its phase from the old denominator to the new one in 32 bits
// and, on an overflow, returns with the ratio changed and the phase not (a read past its table, at the next process):
// both denominators must multiply under 2^32, so this one is small and fixed. a trim moves the numerator in steps of
// 1 / (48000 * in / out): 23 ppm from 44.1 kHz, fine enough for a steering
constexpr std::uint64_t kTrimDen = 48000;

spx_uint32_t frames(std::size_t samples, std::size_t channels) {
  return static_cast<spx_uint32_t>(std::min<std::size_t>(samples / channels, std::numeric_limits<spx_uint32_t>::max()));
}

}  // namespace

void Resampler::Destroy::operator()(SpeexResamplerState_* state) const { speex_resampler_destroy(state); }

Result<Resampler, ResamplerError> Resampler::create(std::size_t channels, std::uint32_t inRate, std::uint32_t outRate) {
  if (channels < 1 || channels > 2)
    return ResamplerError::BadChannels;
  if (inRate < kMinRate || inRate > kMaxRate || outRate < kMinRate || outRate > kMaxRate)
    return ResamplerError::BadRate;
  int error = RESAMPLER_ERR_SUCCESS;
  std::unique_ptr<SpeexResamplerState_, Destroy> state(
      speex_resampler_init(static_cast<spx_uint32_t>(channels), inRate, outRate, kQuality, &error));
  if (!state || error != RESAMPLER_ERR_SUCCESS)
    return ResamplerError::InitFailed;
  // the output starts with the first input sample instead of the filter's half length of silence, so with the
  // flushed tail a stream keeps its length
  speex_resampler_skip_zeros(state.get());
  const int latency = speex_resampler_get_input_latency(state.get());
  return Resampler(std::move(state), channels, static_cast<std::size_t>(std::max(latency, 0)), inRate, outRate);
}

Resampler::Resampler(std::unique_ptr<SpeexResamplerState_, Destroy> state, std::size_t channels,
                     std::size_t tailFrames, std::uint32_t inRate, std::uint32_t outRate)
    : state_(std::move(state)),
      channels_(channels),
      tail_(tailFrames * channels, 0.0f),
      tailLeft_(tailFrames),
      inRate_(inRate),
      outRate_(outRate),
      outPerIn_(static_cast<double>(outRate) / inRate) {}

void Resampler::setTrim(std::int32_t ppm) {
  ppm = std::clamp(ppm, -kMaxTrimPpm, kMaxTrimPpm);
  if (ppm == trimPpm_)
    return;
  trimPpm_ = ppm;
  // a same-rate resampler (a group's playout, steered at 48 kHz) never goes back to the 1/1 ratio once trimmed:
  // speexdsp reduces the ratio by its gcd and rescales the phase to the new denominator, and at 1/1 the fraction of a
  // frame it had is lost, up to a frame at every change (11 frames in 25 flips between 0 and a trim). it stays a step
  // (1 / kTrimDen, ~21 ppm) off on the side it was on instead, which a steering dithers through
  const bool same = inRate_ == outRate_;
  if (ppm == 0 && !(same && trimmedNum_ != 0)) {
    // the rates' own ratio again: the direct filter, exactly
    static_cast<void>(speex_resampler_set_rate(state_.get(), inRate_, outRate_));
    outPerIn_ = static_cast<double>(outRate_) / inRate_;
    return;
  }
  // the ratio in/out: a larger numerator consumes the input faster. speexdsp rescales its phase to the new ratio and
  // keeps its history, so nothing is dropped or repeated at the change
  const auto base = static_cast<double>(kTrimDen) * inRate_ / outRate_;
  auto num = static_cast<std::uint64_t>(std::llround(base * (1.0 + ppm * 1e-6)));
  if (same && num == kTrimDen) {
    const bool up = ppm > 0 || (ppm == 0 && trimmedNum_ > kTrimDen);
    num = up ? kTrimDen + 1 : kTrimDen - 1;
  }
  if (same)
    trimmedNum_ = num;
  static_cast<void>(speex_resampler_set_rate_frac(state_.get(), static_cast<spx_uint32_t>(num),
                                                  static_cast<spx_uint32_t>(kTrimDen), inRate_, outRate_));
  outPerIn_ = static_cast<double>(kTrimDen) / static_cast<double>(num);
}

Resampled Resampler::process(std::span<const float> in, std::span<float> out) {
  spx_uint32_t inFrames = frames(in.size(), channels_);
  spx_uint32_t outFrames = frames(out.size(), channels_);
  // speexdsp reads a null input as zeros, which an empty span may hand it
  if (inFrames == 0 || outFrames == 0)
    return {};
  resample(in.data(), inFrames, out.data(), outFrames);
  pending_ += inFrames * outPerIn_ - outFrames;
  return {.consumed = inFrames, .produced = outFrames};
}

std::size_t Resampler::flush(std::span<float> out) {
  spx_uint32_t inFrames = static_cast<spx_uint32_t>(tailLeft_);
  spx_uint32_t outFrames = frames(out.size(), channels_);
  if (inFrames == 0 || outFrames == 0)
    return 0;
  resample(tail_.data(), inFrames, out.data(), outFrames);
  pending_ += inFrames * outPerIn_ - outFrames;
  tailLeft_ -= inFrames;
  return outFrames;
}

void Resampler::resample(const float* in, std::uint32_t& inFrames, float* out, std::uint32_t& outFrames) {
  // unreachable after a successful create: speexdsp fails a call only when its filter could not be built, which
  // init already refused and only a rate or quality change (never made here) would redo. even then it consumes the
  // input and writes silence, and the counts it leaves say so, so they are returned either way
  static_cast<void>(speex_resampler_process_interleaved_float(state_.get(), in, &inFrames, out, &outFrames));
}

}  // namespace cadence::dsp
