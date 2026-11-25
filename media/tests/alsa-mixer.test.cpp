#include "playback/alsa-mixer.h"

#include <alsa/asoundlib.h>
#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_string.hpp>

#include <vector>

using namespace cadence;
using Catch::Matchers::ContainsSubstring;

namespace {

void quiet(const char*, int, const char*, int, const char*, ...) {}

// alsa prints its own diagnostics for the missing card; the error under test is ours
struct QuietAlsa {
  QuietAlsa() { snd_lib_error_set_handler(quiet); }
  ~QuietAlsa() { snd_lib_error_set_handler(nullptr); }
};

}  // namespace

TEST_CASE("a mixer device without the control reports a named error") {
  const QuietAlsa quietAlsa;
  AlsaMixer mixer("hw:99", "Master");

  const auto set = mixer.setPercent(0);
  REQUIRE(set);
  CHECK_THAT(set->detail, ContainsSubstring("Master on hw:99"));
}

TEST_CASE("percent converts to the control's range as amixer does") {
  CHECK(mixerValueForPercent(0, 0, 255) == 0);
  CHECK(mixerValueForPercent(10, 0, 255) == 26);
  CHECK(mixerValueForPercent(30, 0, 255) == 76);
  CHECK(mixerValueForPercent(100, 0, 255) == 255);
  CHECK(mixerValueForPercent(50, 10, 20) == 15);
}

TEST_CASE("a read-back on a mixer device without the control reports a named error") {
  const QuietAlsa quietAlsa;
  AlsaMixer mixer("hw:99", "Master");

  const auto verified = mixer.verifyPercent(30);
  REQUIRE(verified);
  CHECK_THAT(verified->detail, ContainsSubstring("Master on hw:99"));
}

TEST_CASE("a read-back within one raw step of the value written holds, anything wider does not") {
  const std::vector<long> exact{100, 100};
  CHECK_FALSE(mixerReadBackMismatch(100, exact));
  CHECK_FALSE(mixerReadBackMismatch(100, std::vector<long>{99, 101}));

  const auto low = mixerReadBackMismatch(100, std::vector<long>{98, 100});
  REQUIRE(low);
  CHECK(low->wanted == 100);
  CHECK(low->held == 98);
  CHECK(low->channel == 0);
  CHECK(mixerReadBackMismatch(100, std::vector<long>{102}));
}

TEST_CASE("one channel off by more than a step fails the read-back, and a channel the control lacks is skipped") {
  const auto off = mixerReadBackMismatch(50, std::vector<long>{50, -1, 50, 53});
  REQUIRE(off);
  CHECK(off->channel == 3);
  CHECK(off->held == 53);
  CHECK_FALSE(mixerReadBackMismatch(50, std::vector<long>{-1, 51, -1}));
}
