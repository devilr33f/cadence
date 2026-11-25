#include "audio/wake-gate.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdint>

using namespace cadence;

namespace {

constexpr int kRate = 48000;

constexpr std::uint64_t framesFor(std::uint64_t ms) { return ms * kRate / 1000; }

}  // namespace

TEST_CASE("ignores confirmations in the first 3000 ms of stream time") {
  WakeGate gate(kRate);
  gate.advance(framesFor(kWakeWarmupMs) - 1);
  CHECK_FALSE(gate.confirm());
  CHECK_FALSE(gate.take());

  gate.advance(1);
  CHECK(gate.confirm());
  CHECK(gate.take());
}

TEST_CASE("a confirmation ignored in the warm-up does not start the refractory window") {
  WakeGate gate(kRate);
  gate.advance(framesFor(kWakeWarmupMs - 100));
  gate.confirm();

  gate.advance(framesFor(100));
  gate.confirm();

  CHECK(gate.take());
}

TEST_CASE("the refractory window still applies after the warm-up") {
  WakeGate gate(kRate);
  gate.advance(framesFor(kWakeWarmupMs));
  CHECK(gate.confirm());
  REQUIRE(gate.take());

  gate.advance(framesFor(kWakeRefractoryMs) - 1);
  CHECK_FALSE(gate.confirm());
  CHECK_FALSE(gate.take());

  gate.advance(1);
  CHECK(gate.confirm());
  CHECK(gate.take());
}

TEST_CASE("a gate without a sample rate never wakes") {
  WakeGate gate;
  gate.advance(framesFor(kWakeWarmupMs));
  gate.confirm();
  CHECK_FALSE(gate.take());
}

TEST_CASE("a re-armed gate ignores confirmations for another warm-up from that point in stream time") {
  WakeGate gate(kRate);
  gate.advance(framesFor(kWakeWarmupMs + 2 * kWakeRefractoryMs));
  gate.rearm();
  gate.advance(framesFor(kWakeWarmupMs) - 1);
  CHECK_FALSE(gate.confirm());
  CHECK_FALSE(gate.take());

  gate.advance(1);
  CHECK(gate.confirm());
  CHECK(gate.take());
}
