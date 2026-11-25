#include "audio/spotter-layout.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

#include <cstddef>
#include <string>

using namespace cadence;

namespace {

SpotterChannelCounts single(int type) {
  SpotterChannelCounts counts{};
  counts[static_cast<std::size_t>(type)] = 1;
  return counts;
}

}  // namespace

TEST_CASE("rejects spotter types whose vqe output is not read") {
  const int type = GENERATE(1, 2, 5);

  const auto layout = spotterLayout(single(type));

  REQUIRE_FALSE(layout.ok());
  CHECK(layout.error().code == StockErrorCode::SpotterLayoutUnsupported);
  CHECK(layout.error().detail == "channel type " + std::to_string(type) + " x1");
}

TEST_CASE("rejects a second channel instead of duplicating one output") {
  const int type = GENERATE(0, 3, 4);
  SpotterChannelCounts counts{};
  counts[static_cast<std::size_t>(type)] = 2;

  const auto layout = spotterLayout(counts);

  REQUIRE_FALSE(layout.ok());
  CHECK(layout.error().code == StockErrorCode::SpotterLayoutUnsupported);
  CHECK(layout.error().detail == "channel type " + std::to_string(type) + " x2");
}

TEST_CASE("rejects empty and negative layouts") {
  SpotterChannelCounts counts{};
  SECTION("no channels") {}
  SECTION("negative count") { counts = {1, -1, 0, 0, 0, 0}; }

  const auto layout = spotterLayout(counts);

  REQUIRE_FALSE(layout.ok());
  CHECK(layout.error().code == StockErrorCode::SpotterLayoutUnsupported);
}
