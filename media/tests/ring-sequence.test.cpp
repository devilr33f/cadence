#include "audiod/ring-sequence.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <string>
#include <utility>
#include <vector>

using namespace cadence::audiod;

namespace {

std::shared_ptr<const std::vector<std::int16_t>> samples(std::size_t count, std::int16_t value) {
  return std::make_shared<const std::vector<std::int16_t>>(count, value);
}

// a url that gives what it was handed, pass after pass
class FakeStream final : public RingStream {
 public:
  explicit FakeStream(std::vector<std::int16_t> pass) : pass_(std::move(pass)) {}

  Read read(std::span<std::int16_t> out) override {
    if (stalled)
      return {};
    const auto take = std::min(out.size(), pass_.size() - at_);
    std::copy_n(pass_.begin() + static_cast<std::ptrdiff_t>(at_), take, out.begin());
    at_ += take;
    return {.samples = take, .ended = at_ == pass_.size()};
  }
  void rewind() override {
    ++rewinds;
    at_ = 0;
  }
  [[nodiscard]] std::string_view failure() const override { return failed; }
  void stop() override { stopped = true; }

  bool stalled = false;
  std::string failed;
  int rewinds = 0;
  bool stopped = false;

 private:
  std::vector<std::int16_t> pass_;
  std::size_t at_ = 0;
};

std::vector<std::int16_t> play(RingSequence& ring, std::size_t count) {
  std::vector<std::int16_t> out(count, 99);
  ring.fill(out);
  return out;
}

}  // namespace

TEST_CASE("a ring's parts play in order and loop back to the first") {
  std::vector<RingPart> parts{{.pcm = samples(3, 1)}, {.silence = 2}, {.pcm = samples(1, 2)}};
  RingSequence ring(std::move(parts), true, {{.pcm = samples(1, 7)}}, nullptr);
  CHECK(play(ring, 8) == std::vector<std::int16_t>{1, 1, 1, 0, 0, 2, 1, 1});
  CHECK(play(ring, 4) == std::vector<std::int16_t>{1, 0, 0, 2});
}

TEST_CASE("a ring that does not loop is silent after its last part, and empty parts are skipped") {
  std::vector<RingPart> parts{{.pcm = samples(0, 5)}, {.pcm = samples(2, 1)}, {.silence = 0}};
  RingSequence ring(std::move(parts), false, {}, nullptr);
  CHECK(play(ring, 5) == std::vector<std::int16_t>{1, 1, 0, 0, 0});
  CHECK(play(ring, 2) == std::vector<std::int16_t>{0, 0});
}

TEST_CASE("a stream plays through, and a loop back to it starts it over") {
  auto stream = std::make_shared<FakeStream>(std::vector<std::int16_t>{4, 5, 6});
  std::vector<RingPart> parts{{.pcm = samples(1, 1)}, {.stream = stream}};
  RingSequence ring(std::move(parts), true, {}, nullptr);
  CHECK(play(ring, 8) == std::vector<std::int16_t>{1, 4, 5, 6, 1, 4, 5, 6});
  // entered twice: once as it started, once again after the chime
  CHECK(stream->rewinds == 1);
}

TEST_CASE("a stream entered first is not rewound: its thread already started it") {
  auto stream = std::make_shared<FakeStream>(std::vector<std::int16_t>{4, 5});
  RingSequence ring({{.stream = stream}}, true, {}, nullptr);
  CHECK(play(ring, 3) == std::vector<std::int16_t>{4, 5, 4});
  CHECK(stream->rewinds == 1);
}

TEST_CASE("a stream that fails switches the ring to its fallback for good, told once") {
  auto stream = std::make_shared<FakeStream>(std::vector<std::int16_t>{4, 5, 6, 7});
  std::vector<std::string> told;
  std::vector<RingPart> parts{{.stream = stream}};
  std::vector<RingPart> fallback{{.pcm = samples(2, 8)}, {.silence = 1}};
  RingSequence ring(std::move(parts), true, std::move(fallback), [&](std::string_view code) { told.emplace_back(code); });
  CHECK(play(ring, 2) == std::vector<std::int16_t>{4, 5});
  stream->failed = "http_error";
  CHECK(play(ring, 7) == std::vector<std::int16_t>{8, 8, 0, 8, 8, 0, 8});
  CHECK(told == std::vector<std::string>{"http_error"});
  CHECK(stream->stopped);
  stream->failed = "read_failed";
  play(ring, 3);
  CHECK(told.size() == 1);
}

TEST_CASE("a stream that gives nothing for 3 s falls back as timeout; a slow one does not") {
  auto stream = std::make_shared<FakeStream>(std::vector<std::int16_t>(10, 3));
  std::vector<std::string> told;
  RingSequence ring({{.stream = stream}}, true, {{.pcm = samples(1, 9)}},
                    [&](std::string_view code) { told.emplace_back(code); });
  stream->stalled = true;
  // just under the stall: silence, nothing told
  CHECK(play(ring, kRingStallSamples - 1) == std::vector<std::int16_t>(kRingStallSamples - 1, 0));
  CHECK(told.empty());
  // audio again resets the count
  stream->stalled = false;
  play(ring, 1);
  stream->stalled = true;
  play(ring, kRingStallSamples - 1);
  CHECK(told.empty());
  play(ring, 1);
  REQUIRE(told == std::vector<std::string>{"timeout"});
  CHECK(play(ring, 2) == std::vector<std::int16_t>{9, 9});
}

TEST_CASE("a fallback never streams, and an empty one is silence") {
  auto stream = std::make_shared<FakeStream>(std::vector<std::int16_t>{1});
  auto other = std::make_shared<FakeStream>(std::vector<std::int16_t>{2});
  stream->failed = "timeout";
  RingSequence ring({{.stream = stream}}, true, {{.stream = other}}, nullptr);
  CHECK(play(ring, 2) == std::vector<std::int16_t>{0, 0});
}
