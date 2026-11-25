#pragma once

#include "audio-layout.h"
#include "dsp/resampler.h"
#include "result.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <string_view>
#include <vector>

namespace cadence::audiod {

// the capture stream a board describes in its Configure: the rate and channels (the capture section's, else its
// layout's, else 16 kHz mono; a layout that disagrees is refused), the period and buffer (20 ms and four periods when
// unset), the microphones (channel 0 without a layout) and the speaker reference. "format" when it cannot be a stream
Result<CaptureLayout, std::string_view> boardLayout(const cadence_audiod_v1_CaptureConfig& config);

// the raw microphone for the uplink: the layout's microphones mixed to mono and resampled to 16 kHz; 16 kHz mono
// passes as it is. one thread at a time
class MicDownmix {
 public:
  // "format" for a rate the resampler cannot take
  static Result<MicDownmix, std::string_view> create(const CaptureLayout& layout);

  // appends the 16 kHz samples of these interleaved capture frames to out
  void push(std::span<const std::int16_t> interleaved, std::vector<std::int16_t>& out);

 private:
  MicDownmix(const CaptureLayout& layout, std::optional<dsp::Resampler> resampler);

  std::vector<std::size_t> mics_;
  std::size_t channels_;
  std::optional<dsp::Resampler> resampler_;
  std::vector<float> mono_;
  std::vector<float> resampled_;
};

// a calibration's raw recording: each of the layout's microphones (its speaker references left out) kept apart, the
// first maxChannels of them, resampled to outputRate (16 khz by default, the capture rate for ultrasound); interleaved
// out. one thread at a time
class MicSplit {
 public:
  // "format" for a rate the resampler cannot take
  static Result<MicSplit, std::string_view> create(const CaptureLayout& layout, std::size_t maxChannels,
                                                   std::uint32_t outputRate = 16000);

  [[nodiscard]] std::size_t channels() const { return mics_.size(); }
  // appends the output-rate frames of these interleaved capture frames to out, channels() samples a frame
  void push(std::span<const std::int16_t> interleaved, std::vector<std::int16_t>& out);

 private:
  MicSplit(std::vector<std::size_t> mics, std::size_t channels, std::vector<dsp::Resampler> resamplers);

  std::vector<std::size_t> mics_;
  std::size_t channels_;
  // one per microphone, none at 16 kHz
  std::vector<dsp::Resampler> resamplers_;
  std::vector<float> in_;
  std::vector<std::vector<float>> out_;
};

}  // namespace cadence::audiod
