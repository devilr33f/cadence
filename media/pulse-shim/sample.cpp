#include "pulse-shim/sample.h"

#include "pulse-shim/guard.h"

namespace cadence::pulse {

namespace {

std::optional<std::size_t> sampleSize(pa_sample_format_t format) {
  switch (format) {
    case PA_SAMPLE_U8:
    case PA_SAMPLE_ALAW:
    case PA_SAMPLE_ULAW:
      return 1;
    case PA_SAMPLE_S16LE:
    case PA_SAMPLE_S16BE:
      return 2;
    case PA_SAMPLE_S24LE:
    case PA_SAMPLE_S24BE:
      return 3;
    case PA_SAMPLE_FLOAT32LE:
    case PA_SAMPLE_FLOAT32BE:
    case PA_SAMPLE_S32LE:
    case PA_SAMPLE_S32BE:
    case PA_SAMPLE_S24_32LE:
    case PA_SAMPLE_S24_32BE:
      return 4;
    default:
      return std::nullopt;
  }
}

}  // namespace

std::optional<std::size_t> frameSize(const pa_sample_spec& spec) {
  if (spec.channels < 1 || spec.channels > PA_CHANNELS_MAX || spec.rate < 1 || spec.rate > PA_RATE_MAX)
    return std::nullopt;
  const auto size = sampleSize(spec.format);
  if (!size)
    return std::nullopt;
  return *size * spec.channels;
}

std::optional<pcmframe::Format> wireFormat(const pa_sample_spec& spec) {
  pcmframe::Format format{pcmframe::Encoding::F32Le, spec.channels, spec.rate};
  if (spec.format == PA_SAMPLE_S16LE)
    format.encoding = pcmframe::Encoding::S16Le;
  else if (spec.format != PA_SAMPLE_FLOAT32LE)
    return std::nullopt;
  if (!format.valid())
    return std::nullopt;
  return format;
}

std::size_t usecToBytes(pa_usec_t usec, const pa_sample_spec& spec) {
  const auto frame = frameSize(spec);
  if (!frame)
    return 0;
  // floor(usec * rate / 1e6) without overflowing 64 bits: whole seconds and the rest apart
  const std::uint64_t frames = usec / 1000000 * spec.rate + usec % 1000000 * spec.rate / 1000000;
  return frames > SIZE_MAX / *frame ? SIZE_MAX : static_cast<std::size_t>(frames) * *frame;
}

}  // namespace cadence::pulse

extern "C" std::size_t pa_usec_to_bytes(pa_usec_t t, const pa_sample_spec* spec) {
  using namespace cadence::pulse;
  return guarded(std::size_t{0}, [&] { return spec == nullptr ? 0 : usecToBytes(t, *spec); });
}
