#include "aaudio/aaudio-playback.h"
#include "playback/alsa-player.h"
#include "support/fake-aaudio.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdint>
#include <numeric>
#include <vector>

using namespace cadence;
using cadence::fake_aaudio::FakeApi;

namespace {

constexpr PlaybackFormat kPickle{48000, 2, SampleFormat::S16Le, 960, 7680, 2880};

// interleaved stereo frames numbered from first: both channels carry the frame's number
std::vector<std::int16_t> frames(std::size_t n, std::int16_t first = 1) {
  std::vector<std::int16_t> out(n * 2);
  for (std::size_t i = 0; i < n; ++i)
    out[2 * i] = out[2 * i + 1] = static_cast<std::int16_t>(first + static_cast<std::int16_t>(i));
  return out;
}

bool silent(const std::vector<std::int16_t>& pcm) {
  for (auto s : pcm)
    if (s != 0)
      return false;
  return true;
}

// fills the ring to its target from frame 1
void fillUp(AAudioPlayback& sink) {
  const auto pcm = frames(sink.status().fillFrames);
  const auto w = sink.write(pcm, std::chrono::milliseconds(0));
  REQUIRE(w.kind == PlaybackWrite::Kind::Frames);
  REQUIRE(w.frames == sink.status().fillFrames);
}

}  // namespace

TEST_CASE("aaudio playback opens a low-latency output at the player's rate and channels") {
  FakeApi api;
  AAudioPlayback sink(api);
  const auto granted = sink.open(kPickle);
  REQUIRE(granted.ok());
  REQUIRE(api.requests.size() == 1);
  CHECK(api.requests[0].direction == aaudio::Direction::Output);
  CHECK(api.requests[0].lowLatency);
  CHECK(api.requests[0].sampleRate == 48000);
  CHECK(api.requests[0].channels == 2);
  CHECK(api.stream()->started);
  // the player's fill holds more than two of these small bursts
  CHECK(granted.value().fillFrames == 2880);
  CHECK(granted.value().bufferFrames == 7680);
  CHECK(granted.value().periodFrames == 960);
  CHECK_FALSE(checkPlaybackFormat(kPickle, granted.value()).has_value());
}

TEST_CASE("aaudio playback's ring is sized from the burst, never under three of them") {
  FakeApi api;
  // the stick's callback in s5: one of these underran a 40 ms ring, and two left the player a single burst's slack
  api.burst = 2050;
  AAudioPlayback sink(api);
  const auto granted = sink.open(kPickle);
  REQUIRE(granted.ok());
  CHECK(granted.value().fillFrames == 6150);
  CHECK(granted.value().bufferFrames >= 6150 + 2050);
  CHECK_FALSE(checkPlaybackFormat(kPickle, granted.value()).has_value());

  FakeApi big;
  big.burst = 6000;
  AAudioPlayback large(big);
  const auto g = large.open(kPickle);
  REQUIRE(g.ok());
  CHECK(g.value().fillFrames == 18000);
  CHECK(g.value().bufferFrames >= 24000);
  CHECK_FALSE(checkPlaybackFormat(kPickle, g.value()).has_value());
}

TEST_CASE("aaudio playback refuses what it cannot play exactly") {
  SECTION("another rate") {
    FakeApi api;
    api.grantRate = 44100;
    AAudioPlayback sink(api);
    const auto r = sink.open(kPickle);
    REQUIRE_FALSE(r.ok());
    CHECK(r.error().code == PlaybackErrorCode::FormatMismatch);
  }
  SECTION("no stream") {
    FakeApi api;
    api.openError = "AAUDIO_ERROR_UNAVAILABLE";
    AAudioPlayback sink(api);
    const auto r = sink.open(kPickle);
    REQUIRE_FALSE(r.ok());
    CHECK(r.error().code == PlaybackErrorCode::OpenFailed);
  }
  SECTION("a stream that does not start") {
    FakeApi api;
    api.startFails = true;
    AAudioPlayback sink(api);
    CHECK_FALSE(sink.open(kPickle).ok());
  }
  SECTION("not s16") {
    FakeApi api;
    AAudioPlayback sink(api);
    auto f = kPickle;
    f.sampleFormat = SampleFormat::Unsupported;
    CHECK_FALSE(sink.open(f).ok());
    CHECK(api.opens == 0);
  }
}

TEST_CASE("aaudio playback plays silence until the ring holds its target, then the frames in order") {
  FakeApi api;
  AAudioPlayback sink(api);
  REQUIRE(sink.open(kPickle).ok());
  const auto first = frames(1000);
  REQUIRE(sink.write(first, std::chrono::milliseconds(0)).frames == 1000);
  CHECK(silent(api.pull(192)));
  const auto rest = frames(1880, 1001);
  REQUIRE(sink.write(rest, std::chrono::milliseconds(0)).frames == 1880);
  const auto heard = api.pull(192);
  CHECK(heard == frames(192));
  CHECK(api.pull(192) == frames(192, 193));
}

TEST_CASE("aaudio playback never fills its ring past the target") {
  FakeApi api;
  AAudioPlayback sink(api);
  REQUIRE(sink.open(kPickle).ok());
  const auto pcm = frames(10000);
  const auto w = sink.write(pcm, std::chrono::milliseconds(0));
  CHECK(w.kind == PlaybackWrite::Kind::Frames);
  CHECK(w.frames == 2880);
  CHECK(sink.write(pcm, std::chrono::milliseconds(0)).kind == PlaybackWrite::Kind::Idle);
  api.pull(192);
  CHECK(sink.write(pcm, std::chrono::milliseconds(0)).frames == 192);
}

TEST_CASE("an underrun plays silence, is reported by the next write, and holds until the ring refills") {
  FakeApi api;
  AAudioPlayback sink(api);
  REQUIRE(sink.open(kPickle).ok());
  fillUp(sink);
  // the callback asks for more than the ring holds
  const auto out = api.pull(3000);
  CHECK(std::vector<std::int16_t>(out.begin(), out.begin() + 2 * 2880) == frames(2880));
  CHECK(silent(std::vector<std::int16_t>(out.begin() + 2 * 2880, out.end())));
  CHECK(sink.status().underruns == 1);
  const auto pcm = frames(500, 100);
  const auto w = sink.write(pcm, std::chrono::milliseconds(0));
  CHECK(w.kind == PlaybackWrite::Kind::Recovered);
  CHECK(w.frames == 0);
  REQUIRE(w.error.has_value());
  CHECK(w.error->code == PlaybackErrorCode::Underrun);
  // nothing plays from a part-filled ring: the s5 rule after a flush
  REQUIRE(sink.write(pcm, std::chrono::milliseconds(0)).frames == 500);
  CHECK(silent(api.pull(192)));
  CHECK(sink.status().underruns == 1);
}

TEST_CASE("a rewind takes back the ring, which then holds until it refills") {
  FakeApi api;
  AAudioPlayback sink(api);
  REQUIRE(sink.open(kPickle).ok());
  fillUp(sink);
  api.pull(192);
  const auto rewound = sink.rewind();
  REQUIRE(rewound.ok());
  CHECK(rewound.value() == 2880 - 192);
  CHECK_FALSE(sink.drop().has_value());
  // the ring is empty for the writer at once, before the callback took the cut
  const auto delay = sink.delayFrames();
  REQUIRE(delay.ok());
  CHECK(delay.value() == 192);
  CHECK(silent(api.pull(192)));
  // a deliberate discard is no underrun
  CHECK(sink.status().underruns == 0);
  const auto pcm = frames(2880, 5000);
  CHECK(sink.write(pcm, std::chrono::milliseconds(0)).kind == PlaybackWrite::Kind::Frames);
  CHECK(api.pull(192) == frames(192, 5000));
}

TEST_CASE("the delay is the ring's fill plus what aaudio holds, from its timestamp once it has one") {
  FakeApi api;
  AAudioPlayback sink(api);
  REQUIRE(sink.open(kPickle).ok());
  fillUp(sink);
  for (int i = 0; i < 5; ++i)
    api.pull(192);
  auto* s = api.stream();
  // no timestamp yet: aaudio's unread frames
  s->read = 192 * 2;
  auto d = sink.delayFrames();
  REQUIRE(d.ok());
  CHECK(d.value() == (2880 - 5 * 192) + (5 * 192 - 2 * 192));
  CHECK_FALSE(sink.status().timestamped);
  // frame 100 was heard 1 ms ago: 48 more frames have played since
  s->ts = aaudio::Timestamp{100, api.now - 1'000'000};
  d = sink.delayFrames();
  REQUIRE(d.ok());
  CHECK(d.value() == (2880 - 5 * 192) + (5 * 192 - 148));
  CHECK(sink.status().timestamped);
  // a stale timestamp never makes more played than was handed over
  s->ts = aaudio::Timestamp{0, api.now - 10'000'000'000};
  d = sink.delayFrames();
  REQUIRE(d.ok());
  CHECK(d.value() == 2880 - 5 * 192);
}

TEST_CASE("a stream that dies is opened again by the next write") {
  FakeApi api;
  AAudioPlayback sink(api);
  REQUIRE(sink.open(kPickle).ok());
  fillUp(sink);
  api.disconnect();
  const auto pcm = frames(100);
  const auto w = sink.write(pcm, std::chrono::milliseconds(0));
  CHECK(w.kind == PlaybackWrite::Kind::Recovered);
  CHECK(api.opens == 2);
  CHECK(sink.status().reopens == 1);
  REQUIRE(api.stream() != nullptr);
  CHECK(api.stream()->started);
  // the new stream starts empty
  CHECK(sink.write(pcm, std::chrono::milliseconds(0)).frames == 100);
  CHECK(silent(api.pull(192)));

  SECTION("and a reopen that fails is a failed write") {
    api.disconnect();
    api.openError = "AAUDIO_ERROR_DISCONNECTED";
    const auto f = sink.write(pcm, std::chrono::milliseconds(0));
    CHECK(f.kind == PlaybackWrite::Kind::Failed);
  }
}

TEST_CASE("the player plays through aaudio: the gated silence, then a response") {
  FakeApi api;
  api.burst = 480;
  AAudioPlayback sink(api);
  AlsaPlayer player(sink, AlsaPlayerConfig{960, 7680, 2880, {}});
  REQUIRE(player.start().ok());
  // the player fills the ring with silence on its own thread; the callback pulls at aaudio's pace
  for (int i = 0; i < 2000 && sink.delayFrames().value() < 960; ++i)
    std::this_thread::sleep_for(std::chrono::milliseconds(1));
  const auto status = player.status();
  CHECK(status.running);
  CHECK(status.recoveries == 0);
  player.stop();
  CHECK(api.stream() == nullptr);
}

TEST_CASE("the callback's window says how often it ran and the least the ring held") {
  FakeApi api;
  AAudioPlayback sink(api);
  REQUIRE(sink.open(kPickle).ok());
  // before any callback: the whole ring, no periods
  auto window = sink.takeWindow();
  CHECK(window.period.count == 0);
  CHECK(window.fillMinFrames == sink.status().capacityFrames);
  fillUp(sink);
  for (int i = 0; i < 10; ++i) {
    // one late wake among them
    api.now += i == 7 ? 30'000'000 : 4'000'000;
    api.pull(192);
  }
  window = sink.takeWindow();
  CHECK(window.period.count == 9);
  CHECK(window.period.maxNs == 30'000'000);
  // 95 % of nine is all of them: the late one's bucket, capped at the max
  CHECK(window.period.p95Ns == 30'000'000);
  // the ring held its fill at the first callback, and one burst less at each after
  CHECK(window.fillMinFrames == 2880 - 9 * 192);
  // a new window starts empty
  window = sink.takeWindow();
  CHECK(window.period.count == 0);
  CHECK(window.period.maxNs == 0);
}

TEST_CASE("the delay counts what the callbacks handed over, a burst never counted twice or missed") {
  FakeApi api;
  api.burst = 2050;
  AAudioPlayback sink(api);
  REQUIRE(sink.open(kPickle).ok());
  fillUp(sink);
  auto* s = api.stream();
  // aaudio played nothing yet: everything handed over is still held
  s->read = 0;
  for (int i = 0; i < 3; ++i) {
    api.pull(2050);
    const auto d = sink.delayFrames();
    REQUIRE(d.ok());
    // the ring's fill and aaudio's share move together: the total stays what was written
    CHECK(d.value() == 6150);
    s->written = 0;  // the stream's own count no longer matters: the callbacks' does
  }
}
