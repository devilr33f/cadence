#include "audiod/embedded.h"
#include "support/audiod-client.h"
#include "support/eventually.h"

#include <catch2/catch_test_macros.hpp>

#include <unistd.h>

#include <atomic>
#include <string>
#include <string_view>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;

// the android app's audiod: the daemon on a thread of the host's process, its lines to the host's sink
TEST_CASE("an embedded audiod serves its socket until it is destroyed") {
  const std::string name = "@cadence-embedded-test-" + std::to_string(::getpid());
  std::atomic<int> lines{0};
  auto audiod = EmbeddedAudiod::start(name, [&lines](std::string_view) { ++lines; });
  REQUIRE(audiod);
  CHECK(audiod->running());
  expectPong(connectTo(name).get(), 1);
  CHECK(lines.load() > 0);
  audiod.reset();
  CHECK_FALSE(tryConnect(name));
}

TEST_CASE("an embedded audiod that cannot listen is not started") {
  const std::string name = "@cadence-embedded-taken-" + std::to_string(::getpid());
  auto first = EmbeddedAudiod::start(name, [](std::string_view) {});
  REQUIRE(first);
  CHECK_FALSE(EmbeddedAudiod::start(name, [](std::string_view) {}));
}
