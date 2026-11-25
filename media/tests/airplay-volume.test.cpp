#include "audiod/airplay-volume.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <cstdint>
#include <initializer_list>
#include <limits>

using namespace cadence::audiod;
using namespace cadence::audiod::airplay;
using namespace std::chrono_literals;

namespace {

const auto t0 = SenderClock::time_point{} + 1000s;

// each of the levels, with its neighbours
void expectMarked(const EchoTable& echoes, std::initializer_list<int> levels, SenderClock::time_point at) {
  for (const int level : levels)
    for (int nearby = std::max(0, level - 1); nearby <= std::min(100, level + 1); ++nearby) {
      INFO(nearby);
      CHECK(echoes.isEcho(static_cast<std::uint8_t>(nearby), at));
    }
}

}  // namespace

TEST_CASE("the db map is strict and round-trips every level") {
  for (int percent = 0; percent <= 100; ++percent) {
    INFO(percent);
    CHECK(toPercent(toDb(static_cast<std::uint8_t>(percent))) == percent);
  }
  CHECK(toPercent(-144.0) == 0);
  CHECK(toPercent(-30.0) == 0);
  CHECK(toPercent(0.0) == 100);
  CHECK(toPercent(-15.0) == 50);
  for (const double outside : {-30.0001, 0.0001, -100.0, -143.9, std::numeric_limits<double>::quiet_NaN(),
                               std::numeric_limits<double>::infinity(), -std::numeric_limits<double>::infinity()}) {
    INFO(outside);
    CHECK_FALSE(toPercent(outside));
  }
  CHECK(toDb(0) == -144.0);
  CHECK(toDb(100) == 0.0);
  CHECK(toDb(1) == 0.3 - 30.0);
  CHECK(toDb(200) == 0.0);
}

TEST_CASE("a write from 30 to 90 marks every step the receiver takes, with neighbours, for the window") {
  EchoTable echoes;
  echoes.dispatched(90, toDb(30), t0);
  expectMarked(echoes, {30, 39, 48, 57, 66, 75, 84, 90}, t0);
  CHECK_FALSE(echoes.isEcho(20, t0));
  CHECK_FALSE(echoes.isEcho(35, t0));
  CHECK(echoes.isEcho(90, t0 + kEchoWindow - 1ms));
  CHECK_FALSE(echoes.isEcho(90, t0 + kEchoWindow));
}

TEST_CASE("a write from 90 to mute marks the whole way down, and an unknown start marks every level") {
  EchoTable echoes;
  echoes.dispatched(0, toDb(90), t0);
  expectMarked(echoes, {90, 81, 72, 63, 54, 45, 36, 27, 18, 9, 0}, t0);
  CHECK_FALSE(echoes.isEcho(95, t0));

  for (const auto start : {std::optional<double>{}, std::optional<double>{-80.0}, std::optional<double>{5.0}}) {
    echoes.clear();
    echoes.dispatched(40, start, t0);
    for (int percent = 0; percent <= 100; ++percent)
      CHECK(echoes.isEcho(static_cast<std::uint8_t>(percent), t0));
    CHECK_FALSE(echoes.isEcho(40, t0 + kEchoWindow));
  }

  // mute itself is a usable start
  echoes.clear();
  echoes.dispatched(20, kMuteDb, t0);
  expectMarked(echoes, {0, 9, 18, 20}, t0);
  CHECK_FALSE(echoes.isEcho(50, t0));
}

TEST_CASE("a report that came while a write was in flight marks itself and its neighbours alone") {
  EchoTable echoes;
  echoes.reportDuringWrite(45, t0);
  expectMarked(echoes, {45}, t0);
  CHECK_FALSE(echoes.isEcho(47, t0));
  CHECK_FALSE(echoes.isEcho(43, t0));
  CHECK_FALSE(echoes.isEcho(45, t0 + kEchoWindow));
  // the ends have one neighbour
  echoes.reportDuringWrite(100, t0);
  echoes.reportDuringWrite(0, t0);
  expectMarked(echoes, {100, 0}, t0);
}
