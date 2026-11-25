#include "playback/jitter-buffer.h"
#include "support/jitter-reads.h"
#include "support/samples.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

#include <cstdint>
#include <optional>
#include <string>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;

TEST_CASE("a response that begins after the active one ended plays once the ended one has played out") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(kTarget)) == PushResult::Accepted);
  REQUIRE(buffer.end("r1"));
  REQUIRE(readSamples(buffer, kRead) == ramp(kRead));
  const auto before = buffer.generation();

  REQUIRE(buffer.begin("r2"));

  CHECK(buffer.generation() == before);
  CHECK(buffer.status().activeResponse == "r2");
  CHECK_FALSE(buffer.status().ended);
  CHECK(buffer.push("r1", ramp(10)) == PushResult::Stale);
  CHECK_FALSE(buffer.begin("r1"));
  REQUIRE(buffer.push("r2", constant(kTarget, -7)) == PushResult::Accepted);
  REQUIRE(buffer.end("r2"));
  CHECK(buffer.status().queuedSamples == kTarget * 2 - kRead);

  const auto heard = readSamples(buffer, kTarget * 2 - kRead);
  CHECK(std::vector(heard.begin(), heard.begin() + (kTarget - kRead)) == ramp(kTarget - kRead, static_cast<int>(kRead + 1)));
  CHECK(std::vector(heard.begin() + (kTarget - kRead), heard.end()) == constant(kTarget, -7));
  const auto status = buffer.status();
  CHECK(status.drained == 2);
  CHECK(status.underruns == 0);
  CHECK_FALSE(status.activeResponse.has_value());
}

TEST_CASE("a read moves straight from a drained response to the ended one queued behind it") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(1000)) == PushResult::Accepted);
  REQUIRE(buffer.end("r1"));
  REQUIRE(buffer.begin("r2"));
  REQUIRE(buffer.push("r2", constant(1000, -7)) == PushResult::Accepted);
  REQUIRE(buffer.end("r2"));
  std::vector<std::int16_t> piece(kRead);
  readSamples(buffer, kRead * 2);

  const auto joined = buffer.read(piece);

  CHECK(joined.samples == kRead);
  CHECK(joined.drained);
  CHECK_FALSE(joined.underrun);
  CHECK(std::vector(piece.begin(), piece.begin() + 40) == ramp(40, 961));
  CHECK(std::vector(piece.begin() + 40, piece.end()) == constant(kRead - 40, -7));
  CHECK(buffer.status().activeResponse == "r2");
  CHECK(buffer.status().drained == 1);
}

TEST_CASE("a queued response that has not ended waits for the target after the one before it drains") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(1000)) == PushResult::Accepted);
  REQUIRE(buffer.end("r1"));
  REQUIRE(buffer.begin("r2"));
  REQUIRE(buffer.push("r2", constant(kTarget - 1, -7)) == PushResult::Accepted);
  std::vector<std::int16_t> piece(kRead, -1);
  readSamples(buffer, kRead * 2);

  const auto last = buffer.read(piece);

  CHECK(last.samples == 40);
  CHECK(last.drained);
  CHECK_FALSE(last.underrun);
  CHECK(silent(std::vector(piece.begin() + 40, piece.end())));
  CHECK_FALSE(buffer.status().playing);
  CHECK(silent(readSamples(buffer, kRead)));

  REQUIRE(buffer.push("r2", constant(1, -7)) == PushResult::Accepted);
  CHECK(readSamples(buffer, kRead) == constant(kRead, -7));
}

TEST_CASE("a newer response that begins before the latest one ended drops every queued response") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", constant(kTarget, 1000)) == PushResult::Accepted);
  REQUIRE(buffer.end("r1"));
  REQUIRE(buffer.begin("r2"));
  REQUIRE(buffer.push("r2", constant(kTarget, 2000)) == PushResult::Accepted);
  const auto before = buffer.generation();

  REQUIRE(buffer.begin("r3"));

  CHECK(buffer.generation() > before);
  CHECK(buffer.status().queuedSamples == 0);
  CHECK(buffer.push("r2", constant(10, 2000)) == PushResult::Stale);
  CHECK_FALSE(buffer.begin("r1"));
  CHECK_FALSE(buffer.begin("r2"));
  REQUIRE(buffer.push("r3", ramp(kTarget)) == PushResult::Accepted);
  CHECK(readSamples(buffer, kTarget) == ramp(kTarget));
}

TEST_CASE("cancel drops every queued response, whichever one it names") {
  const auto named = GENERATE(as<std::optional<std::string>>{}, "r1", "r2", std::nullopt);
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", constant(kTarget, 1000)) == PushResult::Accepted);
  REQUIRE(buffer.end("r1"));
  REQUIRE(buffer.begin("r2"));
  REQUIRE(buffer.push("r2", constant(kTarget, 2000)) == PushResult::Accepted);
  const auto before = buffer.generation();

  const bool cancelled = named ? buffer.cancel(*named) : buffer.cancel();

  CHECK(cancelled);

  CHECK(buffer.generation() > before);
  CHECK(buffer.status().queuedSamples == 0);
  CHECK_FALSE(buffer.status().activeResponse.has_value());
  CHECK(silent(readSamples(buffer, kTarget)));
  CHECK_FALSE(buffer.begin("r1"));
  CHECK_FALSE(buffer.begin("r2"));
}

TEST_CASE("cancelFrom drops the named response and the ones queued after it, never one before it") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", constant(kTarget, 1000)) == PushResult::Accepted);
  REQUIRE(buffer.end("r1"));
  REQUIRE(buffer.begin("r2"));
  REQUIRE(buffer.push("r2", constant(kTarget, 2000)) == PushResult::Accepted);
  REQUIRE(buffer.end("r2"));
  REQUIRE(buffer.begin("r3"));
  REQUIRE(buffer.push("r3", constant(kTarget, 3000)) == PushResult::Accepted);
  // r1 is being read
  REQUIRE(readSamples(buffer, kRead) == constant(kRead, 1000));
  const auto before = buffer.generation();

  SECTION("a later one: the one being read plays out, and nothing read is stale") {
    CHECK_FALSE(buffer.cancelFrom("r2"));

    CHECK(buffer.generation() == before);
    CHECK(buffer.status().queuedSamples == kTarget - kRead);
    CHECK(buffer.status().activeResponse == "r1");
    CHECK(buffer.status().ended);
    const auto heard = readSamples(buffer, kTarget);
    CHECK(std::vector(heard.begin(), heard.begin() + (kTarget - kRead)) == constant(kTarget - kRead, 1000));
    CHECK(silent(std::vector(heard.begin() + (kTarget - kRead), heard.end())));
    CHECK(buffer.push("r3", constant(10, 3000)) == PushResult::Stale);
    CHECK_FALSE(buffer.begin("r2"));
    CHECK_FALSE(buffer.begin("r3"));
  }

  SECTION("the one being read: everything goes, and what was read of it is stale") {
    CHECK(buffer.cancelFrom("r1"));

    CHECK(buffer.generation() > before);
    CHECK(buffer.status().queuedSamples == 0);
    CHECK_FALSE(buffer.status().activeResponse.has_value());
    CHECK(silent(readSamples(buffer, kTarget)));
  }

  SECTION("a watched one moves the generation, so its watch settles") {
    REQUIRE(buffer.watch("r2"));

    CHECK(buffer.cancelFrom("r2"));

    CHECK(buffer.generation() > before);
    CHECK(buffer.status().activeResponse == "r1");
    CHECK(buffer.status().queuedSamples == kTarget - kRead);
  }

  SECTION("an id that is not queued is only retired") {
    CHECK_FALSE(buffer.cancelFrom("r9"));

    CHECK(buffer.generation() == before);
    CHECK(buffer.status().queuedSamples == kTarget * 3 - kRead);
    CHECK_FALSE(buffer.begin("r9"));
  }
}

TEST_CASE("cancelFrom of a response nothing was read of leaves the audio already read alone") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", constant(kRead, 1000)) == PushResult::Accepted);
  REQUIRE(buffer.end("r1"));
  REQUIRE(buffer.begin("r2"));
  // below the target, so r2 waits at the front once r1 drained
  REQUIRE(buffer.push("r2", constant(kTarget - 1, 2000)) == PushResult::Accepted);
  REQUIRE(readSamples(buffer, kRead) == constant(kRead, 1000));
  REQUIRE(silent(readSamples(buffer, kRead)));
  const auto before = buffer.generation();

  CHECK_FALSE(buffer.cancelFrom("r2"));

  CHECK(buffer.generation() == before);
  CHECK(buffer.status().queuedSamples == 0);
  CHECK_FALSE(buffer.status().activeResponse.has_value());
  CHECK(buffer.push("r2", constant(1, 2000)) == PushResult::Stale);
}

TEST_CASE("the 500 ms maximum covers every queued response") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(kMaximum - 10)) == PushResult::Accepted);
  REQUIRE(buffer.end("r1"));
  REQUIRE(buffer.begin("r2"));

  CHECK(buffer.push("r2", constant(11, -7)) == PushResult::Overflow);
  CHECK(buffer.push("r2", constant(10, -7)) == PushResult::Accepted);
  CHECK(buffer.status().queuedSamples == kMaximum);
}
