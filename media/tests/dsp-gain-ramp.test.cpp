#include "dsp/gain-ramp.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <cmath>
#include <limits>
#include <vector>

using namespace cadence::dsp;
using Catch::Matchers::WithinAbs;

namespace {

std::vector<float> ones(std::size_t frames) { return std::vector<float>(frames * 2, 1.0f); }

}  // namespace

TEST_CASE("a gain ramps linearly to its target, then holds it") {
  GainRamp ramp;
  ramp.setTarget(0.2f, 100);
  auto block = ones(200);
  ramp.apply(block);
  CHECK_THAT(block[0], WithinAbs(1.0 - 0.8 / 100, 1e-5));
  CHECK_THAT(block[2 * 49], WithinAbs(1.0 - 0.8 * 50 / 100, 1e-4));
  CHECK_THAT(block[2 * 99 + 1], WithinAbs(0.2, 1e-6));
  CHECK_THAT(block[2 * 199], WithinAbs(0.2, 1e-6));
  CHECK(ramp.settled());
}

TEST_CASE("a new target turns the ramp around from where it got to") {
  GainRamp ramp;
  ramp.setTarget(0.0f, 100);
  auto block = ones(50);
  ramp.apply(block);
  CHECK_THAT(ramp.gain(), WithinAbs(0.5, 1e-4));
  ramp.setTarget(1.0f, 50);
  auto next = ones(1);
  ramp.apply(next);
  // no jump: one step up from 0.5
  CHECK_THAT(next[0], WithinAbs(0.51, 1e-4));
}

// rule 7: a duck or a session gain never raises what it is applied to
TEST_CASE("a target is clamped to 0..1 and nan is silence") {
  GainRamp ramp;
  ramp.setTarget(4.0f, 0);
  CHECK(ramp.gain() == 1.0f);
  ramp.setTarget(std::numeric_limits<float>::quiet_NaN(), 0);
  CHECK(ramp.gain() == 0.0f);
  ramp.setTarget(-1.0f, 0);
  CHECK(ramp.gain() == 0.0f);
  auto block = ones(4);
  ramp.apply(block);
  CHECK(block[7] == 0.0f);
}
