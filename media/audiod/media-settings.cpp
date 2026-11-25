#include "audiod/media-settings.h"

#include <algorithm>
#include <cstddef>
#include <iterator>

namespace cadence::audiod {

namespace {

dsp::FilterType toFilter(cadence_audiod_v1_EqType type) {
  switch (type) {
    case cadence_audiod_v1_EqType_EQ_TYPE_PEAKING:
      return dsp::FilterType::Peaking;
    case cadence_audiod_v1_EqType_EQ_TYPE_LOW_SHELF:
      return dsp::FilterType::LowShelf;
    case cadence_audiod_v1_EqType_EQ_TYPE_HIGH_SHELF:
      return dsp::FilterType::HighShelf;
    case cadence_audiod_v1_EqType_EQ_TYPE_LOW_PASS:
      return dsp::FilterType::LowPass;
    case cadence_audiod_v1_EqType_EQ_TYPE_HIGH_PASS:
      return dsp::FilterType::HighPass;
    default:
      return dsp::FilterType::Unspecified;
  }
}

}  // namespace

dsp::EqSettings toEq(const cadence_audiod_v1_EqSettings& eq) {
  dsp::EqSettings settings{.preampDb = eq.preamp_db, .bands = {}};
  const auto count = std::min<std::size_t>(eq.bands_count, std::size(eq.bands));
  for (std::size_t i = 0; i < count; ++i) {
    const auto& band = eq.bands[i];
    settings.bands.push_back({.type = toFilter(band.type), .freqHz = band.freq_hz, .gainDb = band.gain_db, .q = band.q});
  }
  return settings;
}

dsp::DuckMode toDuckMode(cadence_audiod_v1_DuckMode mode) {
  return mode == cadence_audiod_v1_DuckMode_DUCK_MODE_MUFFLE ? dsp::DuckMode::Muffle : dsp::DuckMode::Duck;
}

VizSource toVizSource(cadence_audiod_v1_VizSource source) {
  return source == cadence_audiod_v1_VizSource_VIZ_SOURCE_ALL ? VizSource::All : VizSource::Music;
}

}  // namespace cadence::audiod
