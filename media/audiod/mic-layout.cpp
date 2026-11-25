#include "audiod/mic-layout.h"

#include "audio/opus-frame.h"

#include <algorithm>
#include <cmath>
#include <iterator>
#include <limits>
#include <utility>

namespace cadence::audiod {

namespace {

// more than any board's microphones and references; bounds the buffers a Configure can ask for
constexpr std::uint32_t kMaxChannels = 32;
constexpr std::uint32_t kPeriodsPerSecond = 50;
constexpr std::size_t kBufferPeriods = 4;

// the capture section's value, else the layout's; 0 when the two disagree
std::uint32_t agreed(std::uint32_t own, bool hasLayout, std::uint32_t layout, std::uint32_t fallback) {
  if (!hasLayout || layout == 0)
    return own != 0 ? own : fallback;
  if (own != 0 && own != layout)
    return 0;
  return layout;
}

std::int16_t toSample(float value) {
  return static_cast<std::int16_t>(std::lrint(std::clamp(value * 32768.0f, -32768.0f, 32767.0f)));
}

}  // namespace

Result<CaptureLayout, std::string_view> boardLayout(const cadence_audiod_v1_CaptureConfig& config) {
  constexpr std::string_view kFormat = "format";
  const auto& layout = config.layout;
  const auto rate = agreed(config.sample_rate, config.has_layout, layout.sample_rate, kOpusSampleRate);
  const auto channels = agreed(config.channels, config.has_layout, layout.channels, 1);
  if (rate < dsp::Resampler::kMinRate || rate > dsp::Resampler::kMaxRate || channels == 0 || channels > kMaxChannels)
    return kFormat;
  CaptureLayout out{.device = config.device,
                    .sampleRate = static_cast<int>(rate),
                    .channels = channels,
                    .microphones = {},
                    .speakers = {},
                    .periodFrames = config.period_frames != 0 ? config.period_frames : rate / kPeriodsPerSecond,
                    .bufferFrames = 0};
  out.bufferFrames = config.buffer_frames != 0 ? config.buffer_frames : out.periodFrames * kBufferPeriods;
  if (config.has_layout) {
    out.microphones.assign(layout.mic_channels, layout.mic_channels + layout.mic_channels_count);
    out.speakers.assign(layout.reference_channels, layout.reference_channels + layout.reference_channels_count);
  }
  if (out.microphones.empty())
    out.microphones = {0};
  const auto outside = [&out](std::size_t channel) { return channel >= out.channels; };
  const auto reference = [&out](std::size_t channel) {
    return std::find(out.speakers.begin(), out.speakers.end(), channel) != out.speakers.end();
  };
  if (std::any_of(out.microphones.begin(), out.microphones.end(), outside) ||
      std::any_of(out.speakers.begin(), out.speakers.end(), outside) ||
      std::any_of(out.microphones.begin(), out.microphones.end(), reference))
    return kFormat;
  return out;
}

Result<MicDownmix, std::string_view> MicDownmix::create(const CaptureLayout& layout) {
  if (layout.sampleRate == kOpusSampleRate)
    return MicDownmix(layout, std::nullopt);
  auto resampler = dsp::Resampler::create(1, static_cast<std::uint32_t>(layout.sampleRate), kOpusSampleRate);
  if (!resampler.ok())
    return std::string_view("format");
  return MicDownmix(layout, std::move(resampler.value()));
}

MicDownmix::MicDownmix(const CaptureLayout& layout, std::optional<dsp::Resampler> resampler)
    : mics_(layout.microphones), channels_(layout.channels), resampler_(std::move(resampler)) {}

Result<MicSplit, std::string_view> MicSplit::create(const CaptureLayout& layout, std::size_t maxChannels,
                                                   std::uint32_t outputRate) {
  std::vector<std::size_t> mics;
  for (const auto mic : layout.microphones)
    if (mic < layout.channels && mics.size() < std::max<std::size_t>(maxChannels, 1))
      mics.push_back(mic);
  if (mics.empty())
    return std::string_view("format");
  std::vector<dsp::Resampler> resamplers;
  if (layout.sampleRate != static_cast<int>(outputRate)) {
    for (std::size_t i = 0; i < mics.size(); ++i) {
      auto resampler = dsp::Resampler::create(1, static_cast<std::uint32_t>(layout.sampleRate), outputRate);
      if (!resampler.ok())
        return std::string_view("format");
      resamplers.push_back(std::move(resampler.value()));
    }
  }
  return MicSplit(std::move(mics), layout.channels, std::move(resamplers));
}

MicSplit::MicSplit(std::vector<std::size_t> mics, std::size_t channels, std::vector<dsp::Resampler> resamplers)
    : mics_(std::move(mics)), channels_(channels), resamplers_(std::move(resamplers)), out_(mics_.size()) {}

void MicSplit::push(std::span<const std::int16_t> interleaved, std::vector<std::int16_t>& out) {
  const auto frames = interleaved.size() / channels_;
  const auto mics = mics_.size();
  if (resamplers_.empty()) {
    for (std::size_t frame = 0; frame < frames; ++frame)
      for (const auto mic : mics_)
        out.push_back(interleaved[frame * channels_ + mic]);
    return;
  }
  // each microphone through its own resampler: the same input count gives the same output count
  std::size_t produced = std::numeric_limits<std::size_t>::max();
  for (std::size_t c = 0; c < mics; ++c) {
    in_.resize(frames);
    for (std::size_t frame = 0; frame < frames; ++frame)
      in_[frame] = static_cast<float>(interleaved[frame * channels_ + mics_[c]]) / 32768.0f;
    auto& o = out_[c];
    o.clear();
    std::vector<float> chunk(frames + 64);
    std::span<const float> rest(in_);
    while (!rest.empty()) {
      const auto done = resamplers_[c].process(rest, chunk);
      o.insert(o.end(), chunk.begin(), chunk.begin() + static_cast<std::ptrdiff_t>(done.produced));
      if (done.consumed == 0 && done.produced == 0)
        break;
      rest = rest.subspan(done.consumed);
    }
    produced = std::min(produced, o.size());
  }
  for (std::size_t n = 0; n < produced; ++n)
    for (std::size_t c = 0; c < mics; ++c)
      out.push_back(toSample(out_[c][n]));
}

void MicDownmix::push(std::span<const std::int16_t> interleaved, std::vector<std::int16_t>& out) {
  const auto frames = interleaved.size() / channels_;
  mono_.resize(frames);
  const auto scale = 1.0f / (32768.0f * static_cast<float>(mics_.size()));
  for (std::size_t frame = 0; frame < frames; ++frame) {
    float sum = 0;
    for (const auto mic : mics_)
      sum += static_cast<float>(interleaved[frame * channels_ + mic]);
    mono_[frame] = sum * scale;
  }
  if (!resampler_) {
    std::transform(mono_.begin(), mono_.end(), std::back_inserter(out), toSample);
    return;
  }
  resampled_.resize(frames + 64);
  std::span<const float> rest(mono_);
  while (!rest.empty()) {
    const auto done = resampler_->process(rest, resampled_);
    std::transform(resampled_.begin(), resampled_.begin() + static_cast<std::ptrdiff_t>(done.produced),
                   std::back_inserter(out), toSample);
    if (done.consumed == 0 && done.produced == 0)
      return;
    rest = rest.subspan(done.consumed);
  }
}

}  // namespace cadence::audiod
