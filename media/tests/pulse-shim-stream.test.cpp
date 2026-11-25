// a stream's lifecycle on the pcm socket: its format, the formats it refuses, and how it ends or is replaced
#include "pcmframe/codec.h"
#include "pulse-shim/abi.h"
#include "support/pulse-client.h"

#include <catch2/catch_test_macros.hpp>

#include <sys/socket.h>
#include <sys/un.h>

#include <cstdint>
#include <cstring>
#include <string>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;
using namespace cadence::test_support::pulse;

namespace {

// the send buffer of the shim's socket to path, found as the fd whose peer is path; -1 when there is none
int sendBufferTo(const std::string& path) {
  for (int fd = 0; fd < 1024; ++fd) {
    sockaddr_un peer{};
    socklen_t length = sizeof peer;
    if (::getpeername(fd, reinterpret_cast<sockaddr*>(&peer), &length) != 0 || peer.sun_family != AF_UNIX)
      continue;
    if (std::string(peer.sun_path) != path)
      continue;
    int size = 0;
    socklen_t sizeLength = sizeof size;
    if (::getsockopt(fd, SOL_SOCKET, SO_SNDBUF, &size, &sizeLength) == 0)
      return size;
  }
  return -1;
}

}  // namespace

TEST_CASE("a float stereo 44.1 kHz stream sends its format, then the bytes written") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  CHECK(client.state(s) == PA_STREAM_UNCONNECTED);
  CHECK(client.locked([&] { return pa_stream_get_index(s); }) == PA_INVALID_INDEX);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(s);
  CHECK(seen.stateList() == std::vector<pa_stream_state_t>{PA_STREAM_CREATING, PA_STREAM_READY});
  CHECK(seen.copy(seen.states)[1].second == kLoopThread);
  CHECK(client.locked([&] { return pa_stream_get_index(s); }) != PA_INVALID_INDEX);
  CHECK(client.writable(s) == kDefaultTlength);

  const auto bytes = pattern(0, 32 * 1024);
  REQUIRE(client.write(s, bytes) == 0);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  CHECK(server.pcm(bytes.size()) == bytes);

  // linux reports twice the 32 KiB the shim sets
  CHECK(sendBufferTo(listener.path()) == 2 * 32 * 1024);

  client.finish(s);
  CHECK(seen.stateList().back() == PA_STREAM_TERMINATED);
}

TEST_CASE("an s16 mono 48 kHz stream is accepted, in whole frames only") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_S16LE, 48000, 1);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(s);
  // 250 ms of s16 mono at 48 kHz
  CHECK(client.writable(s) == 24000);
  CHECK(client.write(s, {1, 2, 3}) < 0);
  CHECK(client.error() == PA_ERR_INVALID);
  REQUIRE(client.write(s, {1, 2, 3, 4}) == 0);
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Format{pcmframe::Encoding::S16Le, 1, 48000}});
  CHECK(server.pcm(4) == Bytes{1, 2, 3, 4});

  // the sink input reports the stream's spec, and the stored volume, stereo until set, over its one channel
  const auto index = client.locked([&] { return pa_stream_get_index(s); });
  client.complete(client.locked([&] { return pa_context_get_sink_input_info(client.context(), index, onInfo, seen.ptr()); }));
  const auto infos = seen.copy(seen.infos);
  REQUIRE(infos.size() == 1);
  CHECK(infos[0].index == index);
  CHECK(infos[0].spec.format == PA_SAMPLE_S16LE);
  CHECK(infos[0].spec.rate == 48000);
  CHECK(infos[0].spec.channels == 1);
  CHECK(infos[0].volume == std::vector<pa_volume_t>{PA_VOLUME_NORM});
  client.finish(s);
}

TEST_CASE("a sink input volume is stored and reported, never applied: the bytes on the wire are the bytes written") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_S16LE, 48000, 2);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(s);
  const pa_volume_t thirty = PA_VOLUME_NORM * 30 / 100;
  pa_cvolume volume{};
  REQUIRE(pa_cvolume_set(&volume, 2, thirty) == &volume);
  const auto index = client.locked([&] { return pa_stream_get_index(s); });
  client.complete(client.locked(
      [&] { return pa_context_set_sink_input_volume(client.context(), index, &volume, nullptr, nullptr); }));

  const auto bytes = pattern(0, 4096);
  REQUIRE(client.write(s, bytes) == 0);
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Format{pcmframe::Encoding::S16Le, 2, 48000}});
  CHECK(server.pcm(bytes.size()) == bytes);
  client.complete(
      client.locked([&] { return pa_context_get_sink_input_info(client.context(), index, onInfo, seen.ptr()); }));
  const auto infos = seen.copy(seen.infos);
  REQUIRE(infos.size() == 1);
  CHECK(infos[0].volume == std::vector<pa_volume_t>{thirty, thirty});
  client.finish(s);
}

TEST_CASE("a stream of an invalid spec or a channel map of another count is never made") {
  Listener listener;
  Client client(listener);
  const pa_sample_spec none{PA_SAMPLE_FLOAT32LE, 44100, 0};
  const pa_sample_spec stereo{PA_SAMPLE_FLOAT32LE, 44100, 2};
  pa_channel_map mono{};
  mono.channels = 1;
  CHECK(client.locked([&] { return pa_stream_new(client.context(), "x", &none, nullptr); }) == nullptr);
  CHECK(client.error() == PA_ERR_INVALID);
  CHECK(client.locked([&] { return pa_stream_new(client.context(), "x", &stereo, &mono); }) == nullptr);
  CHECK(client.locked([&] { return pa_stream_new(client.context(), "x", nullptr, nullptr); }) == nullptr);
  auto* proplist = pa_proplist_new();
  auto* s = client.locked([&] { return pa_stream_new_with_proplist(client.context(), "x", &stereo, nullptr, proplist); });
  pa_proplist_free(proplist);
  REQUIRE(s != nullptr);
  client.locked([&] { pa_stream_unref(s); });
}

TEST_CASE("disconnect drops the bytes the stream had not sent") {
  Listener listener;
  Seen first;
  Seen second;
  Client client(listener);
  auto server = listener.accept();
  auto* a = client.stream(first, PA_SAMPLE_FLOAT32LE, 44100, 2);
  REQUIRE(client.connect(a, nullptr, PA_STREAM_START_CORKED) == 0);
  client.ready(a);
  REQUIRE(client.write(a, pattern(0, 4096)) == 0);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Cork{true}});
  client.finish(a);

  auto* b = client.stream(second, PA_SAMPLE_S16LE, 48000, 1);
  REQUIRE(client.connect(b, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(b);
  REQUIRE(client.write(b, {5, 6, 7, 8}) == 0);
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Format{pcmframe::Encoding::S16Le, 1, 48000}});
  CHECK(server.pcm(4) == Bytes{5, 6, 7, 8});
  client.finish(b);
}

TEST_CASE("an s32 or a 96 kHz stream fails not supported and sends nothing") {
  Listener listener;
  Client client(listener);
  auto server = listener.accept();
  for (const auto& [format, rate] : {std::pair{PA_SAMPLE_S32LE, 44100U}, std::pair{PA_SAMPLE_FLOAT32LE, 96000U}}) {
    Seen seen;
    auto* s = client.stream(seen, format, rate, 2);
    CHECK(client.connect(s, nullptr, PA_STREAM_NOFLAGS) < 0);
    CHECK(client.state(s) == PA_STREAM_FAILED);
    CHECK(client.error() == PA_ERR_NOTSUPPORTED);
    CHECK(seen.stateList() == std::vector<pa_stream_state_t>{PA_STREAM_FAILED});
    client.finish(s);
  }
  // a supported stream's format is the first frame
  Seen seen;
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 48000, 2);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_NOFLAGS) == 0);
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Format{pcmframe::Encoding::F32Le, 2, 48000}});
  client.finish(s);
}

TEST_CASE("the server closing the socket fails the stream and the context") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(s);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});

  server.close();
  REQUIRE(eventually([&] { return client.state(s) == PA_STREAM_FAILED; }));
  CHECK(client.contextState() == PA_CONTEXT_FAILED);
  CHECK(client.error() == PA_ERR_CONNECTIONTERMINATED);
  CHECK(seen.copy(seen.states).back() == std::pair{PA_STREAM_FAILED, std::string(kLoopThread)});
  CHECK(client.writable(s) == SIZE_MAX);
  CHECK(client.write(s, pattern(0, 8)) < 0);
  client.finish(s);
}

TEST_CASE("a played report the shim cannot read fails the context with a protocol error") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(s);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  // a frame of the shim's own direction
  server.send({1, 6, 0, 0, 0});
  REQUIRE(eventually([&] { return client.contextState() == PA_CONTEXT_FAILED; }));
  CHECK(client.state(s) == PA_STREAM_FAILED);
  CHECK(client.error() == PA_ERR_PROTOCOL);
  // the shim hung up
  CHECK(server.readSome() == 0);
  client.finish(s);
}

TEST_CASE("a second stream fails the first with killed and sends its own format") {
  Listener listener;
  Seen first;
  Seen second;
  Client client(listener);
  auto server = listener.accept();
  auto* a = client.stream(first, PA_SAMPLE_FLOAT32LE, 44100, 2);
  REQUIRE(client.connect(a, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(a);
  REQUIRE(client.write(a, pattern(0, 4096)) == 0);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  server.pcm(4096);

  auto* b = client.stream(second, PA_SAMPLE_S16LE, 48000, 1);
  REQUIRE(client.connect(b, nullptr, PA_STREAM_NOFLAGS) == 0);
  REQUIRE(eventually([&] { return client.state(a) == PA_STREAM_FAILED; }));
  CHECK(first.copy(first.states).back() == std::pair{PA_STREAM_FAILED, std::string(kLoopThread)});
  CHECK(client.error() == PA_ERR_KILLED);
  CHECK(client.writable(a) == SIZE_MAX);
  client.ready(b);
  REQUIRE(client.write(b, {5, 6, 7, 8}) == 0);
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Format{pcmframe::Encoding::S16Le, 1, 48000}});
  CHECK(server.pcm(4) == Bytes{5, 6, 7, 8});
  CHECK(client.contextState() == PA_CONTEXT_READY);
  client.finish(a);
  client.finish(b);
}

TEST_CASE("usec_to_bytes rounds down to whole frames") {
  const pa_sample_spec floatStereoSpec{PA_SAMPLE_FLOAT32LE, 44100, 2};
  const pa_sample_spec s16Mono{PA_SAMPLE_S16LE, 48000, 1};
  CHECK(pa_usec_to_bytes(1000000, &floatStereoSpec) == 352800);
  CHECK(pa_usec_to_bytes(250000, &floatStereoSpec) == kDefaultTlength);
  CHECK(pa_usec_to_bytes(1, &floatStereoSpec) == 0);
  CHECK(pa_usec_to_bytes(22676, &floatStereoSpec) == 8000);
  CHECK(pa_usec_to_bytes(1000000, &s16Mono) == 96000);
  CHECK(pa_usec_to_bytes(1000000, nullptr) == 0);
}
