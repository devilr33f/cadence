#include "audio/opus-codec.h"
#include "file/wav.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/playback-client.h"
#include "trace/trace-queue.h"
#include "trace/trace.h"

#include <catch2/catch_test_macros.hpp>

#include <poll.h>
#include <sys/eventfd.h>
#include <unistd.h>

#include <atomic>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <numbers>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

constexpr std::string_view kTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01";

// every frame, the marks too, until none came for quiet
std::vector<Envelope> drainAll(int fd, std::chrono::milliseconds quiet = 400ms) {
  std::vector<Envelope> frames;
  for (;;) {
    const auto received = receive(fd, quiet, true);
    if (received.kind != Received::Kind::frame)
      return frames;
    frames.push_back(received.envelope);
  }
}

// every frame, the marks too, up to and with the first that carries body
std::vector<Envelope> until(int fd, pb_size_t body, std::chrono::milliseconds limit = 5s) {
  std::vector<Envelope> frames;
  const auto deadline = std::chrono::steady_clock::now() + limit;
  while (std::chrono::steady_clock::now() < deadline) {
    const auto received = receive(fd, 200ms, true);
    if (received.kind == Received::Kind::closed)
      break;
    if (received.kind != Received::Kind::frame)
      continue;
    frames.push_back(received.envelope);
    if (received.envelope.which_body == body)
      return frames;
  }
  FAIL("the frame never came");
  return frames;
}

std::vector<cadence_audiod_v1_TraceMark> marks(const std::vector<Envelope>& frames) {
  std::vector<cadence_audiod_v1_TraceMark> out;
  for (const auto& envelope : frames)
    if (envelope.which_body == cadence_audiod_v1_Envelope_trace_mark_tag)
      out.push_back(envelope.body.trace_mark);
  return out;
}

std::vector<cadence_audiod_v1_TraceStage> stages(const std::vector<cadence_audiod_v1_TraceMark>& marks) {
  std::vector<cadence_audiod_v1_TraceStage> out;
  for (const auto& mark : marks)
    out.push_back(mark.stage);
  return out;
}

double value(const cadence_audiod_v1_TraceMark& mark, std::string_view key) {
  for (pb_size_t index = 0; index < mark.values_count; ++index)
    if (std::string_view(mark.values[index].key) == key)
      return mark.values[index].value;
  FAIL("no value " << key);
  return 0;
}

Envelope tracedStreamStart(std::string_view id) {
  auto envelope = streamStart(id);
  envelope.body.stream_start.has_trace = true;
  copyText(envelope.body.stream_start.trace.traceparent, kTraceparent);
  return envelope;
}

Envelope captureConfigure(const std::string& wav) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_configure_tag;
  envelope.body.configure.has_capture = true;
  envelope.body.configure.capture.backend = kFile;
  copyText(envelope.body.configure.capture.file_path, wav);
  return envelope;
}

Envelope tracedCaptureStart(std::string_view id) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_capture_start_tag;
  copyText(envelope.body.capture_start.session_id, id);
  envelope.body.capture_start.has_trace = true;
  copyText(envelope.body.capture_start.trace.traceparent, kTraceparent);
  return envelope;
}

Envelope captureStop(std::string_view id) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_capture_stop_tag;
  copyText(envelope.body.capture_stop.session_id, id);
  return envelope;
}

// a second of a 440 Hz tone, 16 kHz mono
std::string toneWav(const AudiodHarness& audiod) {
  const auto path = audiod.file("in.wav");
  auto writer = WavWriter::create(path, {16000, 1});
  REQUIRE(writer.ok());
  std::vector<std::int16_t> pcm(16000);
  for (std::size_t index = 0; index < pcm.size(); ++index)
    pcm[index] = static_cast<std::int16_t>(8000 * std::sin(2 * std::numbers::pi * 440.0 * static_cast<double>(index) / 16000));
  REQUIRE_FALSE(writer.value().write(pcm));
  REQUIRE_FALSE(writer.value().close());
  return path;
}

trace::Mark numbered(std::int64_t n) { return trace::makeMark(trace::Stage::Underrun, trace::Target::Stream, "s", {}, n); }

}  // namespace

TEST_CASE("trace queue: marks come out in order, a full queue drops and counts without waiting", "[trace]") {
  trace::Queue queue;
  for (std::size_t index = 0; index < trace::Queue::kCapacity; ++index)
    REQUIRE(queue.push(numbered(static_cast<std::int64_t>(index) + 1)));
  const auto before = std::chrono::steady_clock::now();
  CHECK_FALSE(queue.push(numbered(1000)));
  CHECK_FALSE(queue.push(numbered(1001)));
  CHECK(std::chrono::steady_clock::now() - before < 50ms);
  CHECK(queue.takeDropped() == 2);
  CHECK(queue.takeDropped() == 0);

  trace::Mark out;
  for (std::size_t index = 0; index < trace::Queue::kCapacity; ++index) {
    REQUIRE(queue.pop(out));
    CHECK(out.atUnixNs == static_cast<std::int64_t>(index) + 1);
    CHECK(out.id.view() == "s");
  }
  CHECK_FALSE(queue.pop(out));
  // room again once drained
  CHECK(queue.push(numbered(7)));
}

TEST_CASE("trace queue: the first push after a rearm wakes the drainer once", "[trace]") {
  const UniqueFd wake(::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK));
  REQUIRE(wake);
  trace::Queue queue(wake.get());
  const auto signals = [&] {
    std::uint64_t count = 0;
    return ::read(wake.get(), &count, sizeof count) == sizeof count ? count : 0;
  };
  queue.push(numbered(1));
  queue.push(numbered(2));
  CHECK(signals() == 1);
  CHECK(signals() == 0);
  queue.rearm();
  queue.push(numbered(3));
  CHECK(signals() == 1);
}

TEST_CASE("trace queue: what several threads push is popped or counted dropped, never lost", "[trace]") {
  trace::Queue queue;
  constexpr int kThreads = 4;
  constexpr int kEach = 5000;
  std::atomic<bool> done{false};
  std::uint64_t popped = 0;
  std::thread drainer([&] {
    trace::Mark out;
    while (!done.load() || queue.pop(out)) {
      while (queue.pop(out))
        ++popped;
    }
  });
  std::vector<std::thread> producers;
  for (int thread = 0; thread < kThreads; ++thread)
    producers.emplace_back([&] {
      for (int index = 0; index < kEach; ++index)
        queue.push(numbered(index + 1));
    });
  for (auto& producer : producers)
    producer.join();
  done.store(true);
  drainer.join();
  CHECK(popped + queue.takeDropped() == kThreads * kEach);
}

TEST_CASE("trace: a traceparent's trace id, and nothing for what is not one", "[trace]") {
  CHECK(trace::traceId(kTraceparent) == "4bf92f3577b34da6a3ce929d0e0e4736");
  CHECK(trace::traceId("").empty());
  CHECK(trace::traceId("00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01").empty());
  CHECK(trace::traceId("00-00000000000000000000000000000000-00f067aa0ba902b7-01").empty());
  CHECK(trace::traceId("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0").empty());
}

TEST_CASE("trace: a tracer is active only in a tracing build", "[trace]") {
  trace::Queue queue;
  const trace::Tracer tracer(&queue);
  CHECK(tracer.active() == trace::kEnabled);
  CHECK_FALSE(trace::Tracer().active());
  tracer.mark(trace::Stage::Underrun, trace::Target::Stream, "s1", {{"device", 1}});
  trace::Mark out;
  CHECK(queue.pop(out) == trace::kEnabled);
}

TEST_CASE("trace: a stream marks its opening, its first sample at the device and its end, before its StreamDone",
          "[trace]") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, audiod.file("out"));
  expectApplied(client.get(), 40, true);

  const auto before = trace::unixNowNs();
  sendFrame(client.get(), tracedStreamStart("r1"));
  sendPackets(client.get(), "r1", opusTone(10, 440, 8000));
  sendFrame(client.get(), streamEnd("r1"));
  const auto frames = until(client.get(), cadence_audiod_v1_Envelope_stream_done_tag);
  CHECK(std::string_view(frames.back().body.stream_done.stream_id) == "r1");
  CHECK(frames.back().body.stream_done.played_ms == 200);
  const auto got = marks(frames);

  if constexpr (!trace::kEnabled) {
    CHECK(got.empty());
    CHECK(marks(drainAll(client.get())).empty());
    return;
  }
  REQUIRE(stages(got) == std::vector{cadence_audiod_v1_TraceStage_TRACE_STAGE_PLAYBACK_OPENED,
                                     cadence_audiod_v1_TraceStage_TRACE_STAGE_FIRST_SAMPLE_PLAYED,
                                     cadence_audiod_v1_TraceStage_TRACE_STAGE_STREAM_DONE});
  std::int64_t last = before;
  for (const auto& mark : got) {
    CHECK(std::string_view(mark.stream_id) == "r1");
    CHECK(std::string_view(mark.session_id).empty());
    CHECK(mark.duration_ns == 0);
    CHECK(mark.at_unix_ns >= last);
    last = mark.at_unix_ns;
  }
  CHECK(last <= trace::unixNowNs());
  CHECK(value(got[1], "buffered_ms") >= 0);
  CHECK(value(got[2], "played_ms") == 200);
  CHECK(value(got[2], "underruns") == 0);
}

TEST_CASE("trace: a refused stream marks only its end, and its trace id is logged", "[trace]") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, audiod.file("out"));
  expectApplied(client.get(), 40, true);
  sendFrame(client.get(), tracedStreamStart("../bad"));
  const auto frames = until(client.get(), cadence_audiod_v1_Envelope_stream_done_tag);
  CHECK(audiod.logs().count(R"("reason":"stream_id","trace":"4bf92f3577b34da6a3ce929d0e0e4736")") == 1);
  if constexpr (trace::kEnabled)
    CHECK(stages(marks(frames)) == std::vector{cadence_audiod_v1_TraceStage_TRACE_STAGE_STREAM_DONE});
  else
    CHECK(marks(frames).empty());
}

TEST_CASE("trace: a capture marks its start, its first opus frame as an interval, its stats and its stop",
          "[trace]") {
  AudiodHarness audiod;
  auto client = connectTo(audiod.socket());
  sendFrame(client.get(), captureConfigure(toneWav(audiod)));
  expectReady(client.get());

  sendFrame(client.get(), tracedCaptureStart("mic-1"));
  std::vector<Envelope> frames;
  // four packets, then the stop, and everything up to the pong after it
  for (int packet = 0; packet < 4; ++packet)
    for (const auto& frame : until(client.get(), cadence_audiod_v1_Envelope_mic_opus_tag))
      frames.push_back(frame);
  sendFrame(client.get(), captureStop("mic-1"));
  sendFrame(client.get(), ping(1));
  for (const auto& frame : until(client.get(), cadence_audiod_v1_Envelope_pong_tag))
    frames.push_back(frame);
  CHECK(audiod.logs().count(R"("msg":"capture_started","trace":"4bf92f3577b34da6a3ce929d0e0e4736")") == 1);
  CHECK(audiod.logs().count(R"("trace":"4bf92f3577b34da6a3ce929d0e0e4736")") == 2);

  const auto got = marks(frames);
  if constexpr (!trace::kEnabled) {
    CHECK(got.empty());
    return;
  }
  // FIRST_OPUS_FRAME follows the packet it marks, the stop's marks come before the pong
  REQUIRE(stages(got) == std::vector{cadence_audiod_v1_TraceStage_TRACE_STAGE_CAPTURE_STARTED,
                                     cadence_audiod_v1_TraceStage_TRACE_STAGE_FIRST_OPUS_FRAME,
                                     cadence_audiod_v1_TraceStage_TRACE_STAGE_CAPTURE_STATS,
                                     cadence_audiod_v1_TraceStage_TRACE_STAGE_CAPTURE_STOPPED});
  for (const auto& mark : got) {
    CHECK(std::string_view(mark.session_id) == "mic-1");
    CHECK(std::string_view(mark.stream_id).empty());
  }
  const auto& started = got[0];
  const auto& first = got[1];
  const auto& stats = got[2];
  const auto& stopped = got[3];
  CHECK(started.duration_ns == 0);
  CHECK(first.at_unix_ns == started.at_unix_ns);
  // a 20 ms packet read at real-time pace
  CHECK(first.duration_ns >= 15'000'000);
  CHECK(value(first, "preroll") == 0);
  CHECK(stats.at_unix_ns == started.at_unix_ns);
  CHECK(stats.duration_ns >= first.duration_ns);
  CHECK(value(stats, "frames") >= 4);
  CHECK(value(stats, "bytes") > 0);
  CHECK(value(stats, "dropped") == 0);
  CHECK(stopped.at_unix_ns == stats.at_unix_ns + stats.duration_ns);
}
