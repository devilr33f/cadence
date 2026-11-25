#include "audiod/media-settings.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence;
using namespace cadence::audiod;

TEST_CASE("an eq maps band by band, a type this build does not know to none") {
  cadence_audiod_v1_EqSettings eq = cadence_audiod_v1_EqSettings_init_zero;
  eq.preamp_db = -3;
  eq.bands_count = 3;
  eq.bands[0] = {cadence_audiod_v1_EqType_EQ_TYPE_LOW_SHELF, 120, 4, 0.5f};
  eq.bands[1] = {cadence_audiod_v1_EqType_EQ_TYPE_HIGH_PASS, 40, 0, 0.707f};
  eq.bands[2] = {static_cast<cadence_audiod_v1_EqType>(7), 1000, 6, 1};

  const auto mapped = toEq(eq);

  CHECK(mapped.preampDb == -3);
  REQUIRE(mapped.bands.size() == 3);
  CHECK(mapped.bands[0].type == dsp::FilterType::LowShelf);
  CHECK(mapped.bands[0].freqHz == 120);
  CHECK(mapped.bands[0].gainDb == 4);
  CHECK(mapped.bands[0].q == 0.5f);
  CHECK(mapped.bands[1].type == dsp::FilterType::HighPass);
  CHECK(mapped.bands[2].type == dsp::FilterType::Unspecified);
}

TEST_CASE("the duck mode and the viz source map to the backend's, anything else to the default") {
  CHECK(toDuckMode(cadence_audiod_v1_DuckMode_DUCK_MODE_UNSPECIFIED) == dsp::DuckMode::Duck);
  CHECK(toDuckMode(cadence_audiod_v1_DuckMode_DUCK_MODE_DUCK) == dsp::DuckMode::Duck);
  CHECK(toDuckMode(cadence_audiod_v1_DuckMode_DUCK_MODE_MUFFLE) == dsp::DuckMode::Muffle);
  CHECK(toDuckMode(static_cast<cadence_audiod_v1_DuckMode>(3)) == dsp::DuckMode::Duck);

  CHECK(toVizSource(cadence_audiod_v1_VizSource_VIZ_SOURCE_UNSPECIFIED) == VizSource::Music);
  CHECK(toVizSource(cadence_audiod_v1_VizSource_VIZ_SOURCE_MUSIC) == VizSource::Music);
  CHECK(toVizSource(cadence_audiod_v1_VizSource_VIZ_SOURCE_ALL) == VizSource::All);
  CHECK(toVizSource(static_cast<cadence_audiod_v1_VizSource>(3)) == VizSource::Music);
}
