#pragma once

#include "dsp/format.h"
#include "result.h"

#include <cstddef>
#include <cstdint>
#include <memory>
#include <span>
#include <vector>

struct SpeexResamplerState_;

namespace cadence::dsp {

enum class ResamplerError { BadChannels, BadRate, InitFailed };

// in frames
struct Resampled {
  std::size_t consumed = 0;
  std::size_t produced = 0;
};

// speexdsp at quality 8 from a source's rate to 48 kHz (a music source) or another output rate (the raw microphone),
// float interleaved; one thread at a time
class Resampler {
 public:
  static constexpr std::uint32_t kMinRate = 8000;
  static constexpr std::uint32_t kMaxRate = 192000;

  // 1 or 2 channels, both rates kMinRate..kMaxRate
  static Result<Resampler, ResamplerError> create(std::size_t channels, std::uint32_t inRate,
                                                  std::uint32_t outRate = static_cast<std::uint32_t>(kRate));

  // as much of in as fits in out
  Resampled process(std::span<const float> in, std::span<float> out);
  // the filter's tail after the last input; call until it produces nothing
  std::size_t flush(std::span<float> out);
  // plays the input this many parts per million faster (positive: fewer output frames per input frame) or slower,
  // for a source whose clock drifts against the output's; clamped to +-kMaxTrimPpm. the filter keeps its phase and
  // history, so the output stays continuous across a change. any trim but 0 runs speexdsp's interpolating filter (some
  // four times the direct one's work), so a source that never drifts never pays for it
  void setTrim(std::int32_t ppm);
  [[nodiscard]] std::int32_t trim() const { return trimPpm_; }
  // input frames the filter holds back before they come out (its input latency): what is pushed next is that much
  // later than the output written so far
  [[nodiscard]] std::size_t latencyFrames() const { return tail_.size() / channels_; }
  // the same to a fraction of a frame, steering included: the output frames the consumed input is still owed
  [[nodiscard]] double pendingFrames() const { return pending_; }

  static constexpr std::int32_t kMaxTrimPpm = 20000;

 private:
  struct Destroy {
    void operator()(SpeexResamplerState_* state) const;
  };

  Resampler(std::unique_ptr<SpeexResamplerState_, Destroy> state, std::size_t channels, std::size_t tailFrames,
            std::uint32_t inRate, std::uint32_t outRate);

  void resample(const float* in, std::uint32_t& inFrames, float* out, std::uint32_t& outFrames);

  std::unique_ptr<SpeexResamplerState_, Destroy> state_;
  std::size_t channels_;
  // the zeros flush feeds, as many frames as the filter's input latency
  std::vector<float> tail_;
  std::size_t tailLeft_;
  std::uint32_t inRate_;
  std::uint32_t outRate_;
  std::int32_t trimPpm_ = 0;
  // output frames per input frame at the ratio in force, and the output the input consumed so far is still owed: where
  // the next input frame lands, past what was produced, to a fraction of a frame
  double outPerIn_ = 1;
  double pending_ = 0;
  // a same-rate resampler's numerator once it was trimmed, 0 before
  std::uint64_t trimmedNum_ = 0;
};

}  // namespace cadence::dsp
