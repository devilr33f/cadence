#include "support/eventually.h"
#include "support/http-server.h"
#include "url/ring-stream.h"
#include "url/url-source.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <string>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

void put16(std::string& out, std::uint16_t value) {
  out.push_back(static_cast<char>(value & 0xff));
  out.push_back(static_cast<char>(value >> 8));
}

void put32(std::string& out, std::uint32_t value) {
  put16(out, static_cast<std::uint16_t>(value & 0xffff));
  put16(out, static_cast<std::uint16_t>(value >> 16));
}

// a mono s16 wav whose sample i is i % 1000 + 1, at rate
std::string wav(std::size_t samples, std::uint32_t rate) {
  std::string out = "RIFF";
  put32(out, static_cast<std::uint32_t>(36 + samples * 2));
  out += "WAVEfmt ";
  put32(out, 16);
  put16(out, 1);
  put16(out, 1);
  put32(out, rate);
  put32(out, rate * 2);
  put16(out, 2);
  put16(out, 16);
  out += "data";
  put32(out, static_cast<std::uint32_t>(samples * 2));
  for (std::size_t i = 0; i < samples; ++i)
    put16(out, static_cast<std::uint16_t>(i % 1000 + 1));
  return out;
}

// reads until count samples came or the pass ended
std::vector<std::int16_t> readPass(audiod::RingStream& stream, std::size_t count, bool& ended) {
  std::vector<std::int16_t> heard;
  ended = false;
  const auto deadline = std::chrono::steady_clock::now() + 10s;
  while (heard.size() < count && !ended && std::chrono::steady_clock::now() < deadline) {
    std::vector<std::int16_t> chunk(480);
    const auto read = stream.read(chunk);
    heard.insert(heard.end(), chunk.begin(), chunk.begin() + static_cast<std::ptrdiff_t>(read.samples));
    ended = read.ended;
    if (read.samples == 0 && !ended)
      std::this_thread::sleep_for(1ms);
  }
  return heard;
}

}  // namespace

TEST_CASE("a ring's url plays whole with its authorization, and a short one replays from memory") {
  HttpServer server;
  server.route("/ring/a", {.contentType = "audio/wav", .body = wav(4800, 48000)});
  auto stream = url::openRingStream({.url = server.url("/ring/a"), .authorization = "Bearer t0k", .caFile = ""});
  bool ended = false;
  const auto first = readPass(*stream, 100000, ended);
  REQUIRE(ended);
  REQUIRE(first.size() == 4800);
  CHECK(first[0] == 1);
  CHECK(first[999] == 1000);
  CHECK(stream->failure().empty());
  stream->rewind();
  const auto second = readPass(*stream, 100000, ended);
  CHECK(ended);
  CHECK(second == first);
  CHECK(server.requests("/ring/a") == 1);
  CHECK(server.authorizations("/ring/a") == std::vector<std::string>{"Bearer t0k"});
}

TEST_CASE("a ring's url longer than it keeps is fetched again for the next pass") {
  HttpServer server;
  server.route("/ring/b", {.contentType = "audio/wav", .body = wav(9600, 48000)});
  auto stream = url::openRingStream({.url = server.url("/ring/b"), .authorization = "", .caFile = ""},
                                    {.aheadSamples = 2400, .keepSamples = 4800});
  bool ended = false;
  CHECK(readPass(*stream, 100000, ended).size() == 9600);
  REQUIRE(ended);
  stream->rewind();
  CHECK(readPass(*stream, 100000, ended).size() == 9600);
  CHECK(server.requests("/ring/b") == 2);
}

TEST_CASE("a ring's url fails with a fixed code: not found, another rate, or silence past the stall") {
  HttpServer server;
  server.route("/ring/44k", {.contentType = "audio/wav", .body = wav(4410, 44100)});
  server.route("/ring/stall", {.stall = true});

  auto missing = url::openRingStream({.url = server.url("/ring/none"), .authorization = "", .caFile = ""});
  REQUIRE(eventually([&] { return !missing->failure().empty(); }));
  CHECK(missing->failure() != "timeout");

  auto other = url::openRingStream({.url = server.url("/ring/44k"), .authorization = "", .caFile = ""});
  REQUIRE(eventually([&] { return !other->failure().empty(); }));
  CHECK(other->failure() == "unsupported");

  auto stalled = url::openRingStream({.url = server.url("/ring/stall"), .authorization = "", .caFile = ""},
                                     {.stall = 300ms});
  REQUIRE(eventually([&] { return !stalled->failure().empty(); }));
  CHECK(stalled->failure() == "timeout");

  auto refused = url::openRingStream({.url = "file:///etc/passwd", .authorization = "", .caFile = ""});
  CHECK(refused->failure() == "invalid");
}

TEST_CASE("a stopped ring stream reads nothing and lets its thread go") {
  HttpServer server;
  server.route("/ring/stall", {.stall = true});
  auto stream = url::openRingStream({.url = server.url("/ring/stall"), .authorization = "", .caFile = ""});
  stream->stop();
  std::vector<std::int16_t> chunk(10);
  CHECK(stream->read(chunk).samples == 0);
  stream.reset();
  CHECK(url::decodersFinished(5s));
}
