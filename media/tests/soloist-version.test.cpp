#include "audiod/soloist-version.h"

#include <catch2/catch_test_macros.hpp>

#include <string>
#include <string_view>

using namespace cadence::audiod;

namespace {

constexpr std::string_view kLine = "soloist 1.3.10.18 build 1793059327 (20261027) (g19412df6ea) (linux/aarch64)";

}  // namespace

TEST_CASE("the real version line parses") {
  const SoloistBuild expected{.version = "1.3.10.18", .epoch = 1'793'059'327, .date = "20261027"};
  CHECK(parseSoloistVersion(kLine) == expected);
  CHECK(parseSoloistVersion(std::string(kLine) + "\n") == expected);
  CHECK(parseSoloistVersion(std::string(kLine) + "\r\n") == expected);
  // a warning ahead of it does not hide it
  CHECK(parseSoloistVersion("client expires in 80 days\n" + std::string(kLine) + "\n") == expected);
  CHECK(parseSoloistVersion("soloist  1.3.10.18\tbuild 1793059327 (20261027)") == expected);
}

TEST_CASE("anything else is no build") {
  for (const std::string_view text : {
           "",
           "\n",
           "soloist",
           "1.3.10.18 build 1793059327 (20261027)",
           "soloist 1.3.10.18 built 1793059327 (20261027)",
           "soloist 1.3.10.18 build 1793059327",
           "soloist 1.3.10.18 build 1793059327 20261027",
           "soloist 1.3.10.18 build 1793059327 (2026102)",
           "soloist 1.3.10.18 build 1793059327 (2026102x)",
           "soloist 1.3.10.18 build 1793059327 (202610277)",
           "soloist 1.3.10.18 build -1 (20261027)",
           "soloist 1.3.10.18 build +1793059327 (20261027)",
           "soloist 1.3.10.18 build 99999999999999999999999 (20261027)",
           // beyond what the status's int64 carries
           "soloist 1.3.10.18 build 9223372036854775808 (20261027)",
           "soloist 1.3.10.18 build 1.5 (20261027)",
           "soloist 1.3/10 build 1793059327 (20261027)",
           "soloist 1.3.10.18.1.1.1.1.1.1.1.1.1.1.1.1.1.1.1.1 build 1793059327 (20261027)",
           "Segmentation fault",
       }) {
    INFO(text);
    CHECK_FALSE(parseSoloistVersion(text));
  }
}
