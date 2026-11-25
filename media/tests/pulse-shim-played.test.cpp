// the read index, started and underflow as the server's played reports move them, and the timing info
#include "pcmframe/codec.h"
#include "pulse-shim/abi.h"
#include "support/pulse-client.h"

#include <catch2/catch_test_macros.hpp>

#include <sys/time.h>

#include <chrono>
#include <cstdint>
#include <string>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;
using namespace cadence::test_support::pulse;

namespace {

std::int64_t usec(const timeval& t) { return static_cast<std::int64_t>(t.tv_sec) * 1000000 + t.tv_usec; }

std::int64_t nowUsec() {
  timeval now{};
  ::gettimeofday(&now, nullptr);
  return usec(now);
}

bool allOnLoop(const std::vector<std::string>& threads) {
  for (const auto& thread : threads)
    if (thread != kLoopThread)
      return false;
  return true;
}

}  // namespace

TEST_CASE("started fires once after an uncork and underflow when the buffer runs dry") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_START_CORKED) == 0);
  client.ready(s);
  REQUIRE(client.write(s, pattern(0, 4096)) == 0);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Cork{true}});

  client.cork(s, false, seen);
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Cork{false}});
  server.pcm(4096);
  client.roundTrip(s);
  CHECK(seen.startedCount() == 1);
  REQUIRE(client.write(s, pattern(4096, 4096)) == 0);
  server.pcm(4096);

  // the server has played half: not dry yet
  server.played(4096);
  CHECK(eventually([&] { return client.readIndex(s) == 4096; }));
  client.roundTrip(s);
  CHECK(seen.underflowCount() == 0);
  server.played(8192);
  CHECK(eventually([&] { return seen.underflowCount() == 1; }));

  // playback starts again after the underrun, and since_underrun counts from there
  REQUIRE(client.write(s, pattern(8192, 4096)) == 0);
  server.pcm(4096);
  client.roundTrip(s);
  CHECK(seen.startedCount() == 2);
  server.played(8192 + 1024);
  CHECK(eventually([&] { return client.readIndex(s) == 8192 + 1024; }));
  CHECK(client.freshTiming(s).since_underrun == 1024);
  CHECK(seen.underflowCount() == 1);
  CHECK(allOnLoop(seen.copy(seen.started)));
  CHECK(allOnLoop(seen.copy(seen.underflows)));
  client.finish(s);
}

TEST_CASE("update_timing_info completes on the loop with the read index from played") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(s);
  CHECK(client.locked([&] { return pa_stream_get_timing_info(s); }) == nullptr);

  REQUIRE(client.write(s, pattern(0, 8192)) == 0);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  server.pcm(8192);
  const auto before = nowUsec();
  server.played(4096);
  const auto timing = [&] { return client.locked([&] { return pa_stream_get_timing_info(s); }); };
  CHECK(eventually([&] {
    client.complete(client.locked([&] { return pa_stream_update_timing_info(s, onSuccess, seen.ptr()); }));
    const auto* info = timing();
    return info != nullptr && info->read_index == 4096;
  }));
  const auto successes = seen.copy(seen.successes);
  CHECK_FALSE(successes.empty());
  for (const auto& [success, thread] : successes) {
    CHECK(success == 1);
    CHECK(thread == kLoopThread);
  }
  const auto* info = timing();
  auto snapshot = *info;
  CHECK(snapshot.write_index == 8192);
  CHECK(snapshot.playing == 1);
  CHECK(snapshot.since_underrun == 4096);
  CHECK(snapshot.synchronized_clocks == 1);
  CHECK(snapshot.read_index_corrupt == 0);

  // the timestamp is when the report arrived, so a later update without one keeps it
  const auto reported = usec(snapshot.timestamp);
  CHECK(reported >= before);
  REQUIRE(eventually([&] { return nowUsec() > reported; }));
  CHECK(usec(client.freshTiming(s).timestamp) == reported);

  // only write_index follows a write without an update
  REQUIRE(client.write(s, pattern(8192, 4096)) == 0);
  CHECK(timing() == info);
  snapshot = *info;
  CHECK(snapshot.write_index == 12288);
  CHECK(snapshot.read_index == 4096);

  // a cork leaves the read index out of date until the next update
  client.cork(s, true, seen);
  CHECK(client.locked([&] { return pa_stream_get_timing_info(s)->read_index_corrupt; }) == 1);
  CHECK(client.freshTiming(s).read_index_corrupt == 0);
  CHECK(client.freshTiming(s).playing == 0);
  client.finish(s);
}

TEST_CASE("an auto timing update stream has a fresh snapshot at every look") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_S16LE, 48000, 2);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_AUTO_TIMING_UPDATE) == 0);
  client.ready(s);
  const auto readIndex = [&] {
    return client.locked([&] {
      const auto* info = pa_stream_get_timing_info(s);
      return info == nullptr ? std::int64_t{-1} : info->read_index;
    });
  };
  CHECK(readIndex() == 0);
  REQUIRE(client.write(s, pattern(0, 4096)) == 0);
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Format{pcmframe::Encoding::S16Le, 2, 48000}});
  server.pcm(4096);
  server.played(2048);
  CHECK(eventually([&] { return readIndex() == 2048; }));
  CHECK(client.locked([&] { return pa_stream_get_timing_info(s)->write_index; }) == 4096);
  client.finish(s);
}

TEST_CASE("a second stream counts its read index from the bytes sent before its format, stale reports ignored") {
  Listener listener;
  Seen first;
  Seen second;
  Client client(listener);
  auto server = listener.accept();
  auto* a = client.stream(first, PA_SAMPLE_FLOAT32LE, 44100, 2);
  REQUIRE(client.connect(a, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(a);
  REQUIRE(client.write(a, pattern(0, 8192)) == 0);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  server.pcm(8192);
  client.finish(a);

  auto* b = client.stream(second, PA_SAMPLE_S16LE, 48000, 1);
  REQUIRE(client.connect(b, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(b);
  REQUIRE(client.write(b, pattern(0, 4096)) == 0);
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Format{pcmframe::Encoding::S16Le, 1, 48000}});
  server.pcm(4096);

  // reports up to the base still count the first stream's bytes: b's read index stays put, nothing runs dry
  server.played(6144);
  server.played(8192);
  server.played(8192 + 1024);
  CHECK(eventually([&] { return client.readIndex(b) == 1024; }));
  client.roundTrip(b);
  CHECK(second.underflowCount() == 0);
  CHECK(second.startedCount() == 1);

  // a report below one already seen never moves the read index back, and one past what was sent stops there
  server.played(8192 + 512);
  server.played(8192 + 8192);
  CHECK(eventually([&] { return second.underflowCount() == 1; }));
  CHECK(client.readIndex(b) == 4096);
  client.finish(b);
}

TEST_CASE("a played report after a flush catches up without an underflow") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(s);
  REQUIRE(client.write(s, pattern(0, 4096)) == 0);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  server.pcm(4096);
  client.flush(s, seen);
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Flush{}});

  // the server dropped what it held: its report reaches the write index, a flush and not an underrun
  server.played(4096);
  CHECK(eventually([&] { return client.readIndex(s) == 4096; }));
  client.roundTrip(s);
  CHECK(seen.underflowCount() == 0);

  // the next write ends the dry spell without starting playback again; running dry then is an underrun
  REQUIRE(client.write(s, pattern(4096, 4096)) == 0);
  server.pcm(4096);
  client.roundTrip(s);
  CHECK(seen.startedCount() == 1);
  server.played(8192);
  CHECK(eventually([&] { return seen.underflowCount() == 1; }));
  client.finish(s);
}

TEST_CASE("bytes a flush dropped before they went out count as read once the server is past the flush") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_START_CORKED) == 0);
  client.ready(s);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Cork{true}});
  // corked: these wait in the shim, and the flush drops them unsent
  REQUIRE(client.write(s, pattern(0, 4096)) == 0);
  client.flush(s, seen);
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Flush{}});
  CHECK(client.readIndex(s) == 4096);
  const auto timing = client.freshTiming(s);
  CHECK(timing.write_index == 4096);
  client.finish(s);
}
