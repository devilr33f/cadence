#include "url/pcm-convert.h"

#include <cstring>

namespace cadence::url {

namespace {

constexpr float kS16Scale = 1.0f / 32768.0f;
constexpr double kS32Scale = 1.0 / 2147483648.0;

float toFloat(float sample) { return sample; }
float toFloat(double sample) { return static_cast<float>(sample); }
float toFloat(std::int16_t sample) { return static_cast<float>(sample) * kS16Scale; }
float toFloat(std::int32_t sample) { return static_cast<float>(static_cast<double>(sample) * kS32Scale); }

// planes may be unaligned for T, so each sample is copied out
template <typename T>
T sampleAt(const std::uint8_t* plane, std::size_t index) {
  T sample;
  std::memcpy(&sample, plane + index * sizeof(T), sizeof(T));
  return sample;
}

template <typename T>
void convert(const std::uint8_t* const* planes, bool planar, std::size_t channels, std::size_t frames,
             std::vector<float>& out) {
  out.resize(frames * channels);
  for (std::size_t frame = 0; frame < frames; ++frame)
    for (std::size_t channel = 0; channel < channels; ++channel)
      out[frame * channels + channel] = toFloat(planar ? sampleAt<T>(planes[channel], frame)
                                                       : sampleAt<T>(planes[0], frame * channels + channel));
}

}  // namespace

bool toInterleavedFloat(AVSampleFormat format, const std::uint8_t* const* planes, std::size_t channels,
                        std::size_t frames, std::vector<float>& out) {
  switch (format) {
    case AV_SAMPLE_FMT_FLT:
    case AV_SAMPLE_FMT_FLTP:
      convert<float>(planes, format == AV_SAMPLE_FMT_FLTP, channels, frames, out);
      return true;
    case AV_SAMPLE_FMT_DBL:
    case AV_SAMPLE_FMT_DBLP:
      convert<double>(planes, format == AV_SAMPLE_FMT_DBLP, channels, frames, out);
      return true;
    case AV_SAMPLE_FMT_S16:
    case AV_SAMPLE_FMT_S16P:
      convert<std::int16_t>(planes, format == AV_SAMPLE_FMT_S16P, channels, frames, out);
      return true;
    case AV_SAMPLE_FMT_S32:
    case AV_SAMPLE_FMT_S32P:
      convert<std::int32_t>(planes, format == AV_SAMPLE_FMT_S32P, channels, frames, out);
      return true;
    default:
      return false;
  }
}

}  // namespace cadence::url
