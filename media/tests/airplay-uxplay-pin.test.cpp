#include "audiod/airplay-uxplay-pin.h"
#include "support/temp-dir.h"

#include <catch2/catch_test_macros.hpp>

#include <sys/stat.h>

#include <fstream>
#include <string>

using namespace cadence::audiod::uxplay;
using namespace std::chrono_literals;

namespace {

// 32 bytes of base64
std::string key(char fill) { return std::string(43, fill) + "="; }

}  // namespace

TEST_CASE("a key is base64 of 32 bytes") {
  CHECK(plausibleKey(key('A')));
  CHECK(plausibleKey(std::string(44, 'b')));
  CHECK_FALSE(plausibleKey("short"));
  CHECK_FALSE(plausibleKey(std::string(43, 'A') + "!"));
  CHECK_FALSE(plausibleKey(std::string(42, 'A') + "=A"));
}

TEST_CASE("a sender that paired is known again after a restart, from a private file") {
  cadence::test_support::TempDir dir;
  const auto path = dir.file("paired");
  {
    PairedSenders paired(path);
    CHECK_FALSE(paired.known(key('A')));
    CHECK(paired.add(key('A')));
    CHECK(paired.known(key('A')));
    CHECK_FALSE(paired.add("not a key"));
  }
  struct stat info {};
  REQUIRE(::stat(path.c_str(), &info) == 0);
  CHECK((info.st_mode & 0777) == 0600);
  PairedSenders again(path);
  CHECK(again.known(key('A')));
  CHECK_FALSE(again.known(key('B')));
  CHECK(again.size() == 1);
}

TEST_CASE("what the paired file holds that is no key is skipped") {
  cadence::test_support::TempDir dir;
  const auto path = dir.file("paired");
  {
    std::ofstream out(path);
    out << "garbage\n" << key('C') << "\n\n" << key('C') << "\n";
  }
  PairedSenders paired(path);
  CHECK(paired.size() == 1);
  CHECK(paired.known(key('C')));
}

TEST_CASE("the paired senders are bounded, the oldest going first") {
  PairedSenders paired("");
  for (std::size_t i = 0; i <= kMaxPaired; ++i) {
    auto k = std::string(40, 'a') + std::to_string(1000 + i);
    REQUIRE(paired.add(k));
  }
  CHECK(paired.size() == kMaxPaired);
  CHECK_FALSE(paired.known(std::string(40, 'a') + "1000"));
  CHECK(paired.known(std::string(40, 'a') + std::to_string(1000 + kMaxPaired)));
}

TEST_CASE("a pin is shown until hidden or until it expires, and only 4 digits are one") {
  PinPrompt prompt;
  const auto now = PinPrompt::Clock::now();
  CHECK_FALSE(prompt.show("12a4", now));
  CHECK_FALSE(prompt.show("12345", now));
  CHECK_FALSE(prompt.active());
  REQUIRE(prompt.show("0427", now));
  CHECK(prompt.active());
  CHECK(prompt.pin() == "0427");
  CHECK(prompt.deadline() == now + kPinShown);
  CHECK_FALSE(prompt.expire(now + 59s));
  CHECK(prompt.expire(now + 60s));
  CHECK_FALSE(prompt.active());
  CHECK_FALSE(prompt.expire(now + 61s));
  REQUIRE(prompt.show("1111", now));
  CHECK(prompt.hide());
  CHECK_FALSE(prompt.hide());
  CHECK(prompt.pin().empty());
}
