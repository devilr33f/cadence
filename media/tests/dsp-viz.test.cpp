#include "dsp/viz.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cmath>
#include <cstddef>
#include <span>
#include <vector>

using namespace cadence::dsp;
using namespace cadence::test_support;

namespace {

void feed(VizBands& viz, std::span<const float> samples, std::size_t chunk) {
  for (std::size_t first = 0; first < samples.size(); first += chunk)
    viz.push(samples.subspan(first, std::min(chunk, samples.size() - first)));
}

}  // namespace

TEST_CASE("a 1 kHz full-scale sine lights its band and leaves the far ones dark") {
  VizBands viz;
  feed(viz, sine(1000, 1.0, 2 * kVizHop), 480);
  REQUIRE(viz.ready());
  const auto bands = viz.bands();
  // the edges are 60 Hz * (16000 / 60)^(k / 16)
  const auto band = static_cast<std::size_t>(16 * std::log(1000.0 / 60) / std::log(16000.0 / 60));
  CHECK(static_cast<int>(bands[band]) >= 240);
  for (std::size_t k = 0; k < kVizBands; ++k) {
    CAPTURE(k, static_cast<int>(bands[k]));
    if (k + 2 <= band || k >= band + 2)
      CHECK(static_cast<int>(bands[k]) < 100);
  }
}

TEST_CASE("silence reads all zeros") {
  VizBands viz;
  feed(viz, std::vector<float>(kVizHop, 0.0f), 480);
  REQUIRE(viz.ready());
  CHECK(viz.bands() == VizFrame{});
}

TEST_CASE("exactly one frame comes per 1600 samples fed") {
  for (const std::size_t chunk : {std::size_t{1}, std::size_t{333}, std::size_t{480}, std::size_t{1600}}) {
    CAPTURE(chunk);
    VizBands viz;
    const auto samples = sine(440, 0.5, 5 * kVizHop + kVizHop / 2);
    std::size_t frames = 0;
    for (std::size_t first = 0; first < samples.size(); first += chunk) {
      viz.push(std::span(samples).subspan(first, std::min(chunk, samples.size() - first)));
      if (viz.ready()) {
        ++frames;
        viz.bands();
        CHECK(!viz.ready());
      }
    }
    CHECK(frames == 5);
  }

  VizBands viz;
  const std::vector<float> quiet(kVizHop - 1, 0.0f);
  viz.push(quiet);
  CHECK(!viz.ready());
  const float last = 0;
  viz.push(std::span(&last, 1));
  CHECK(viz.ready());
}
