// the data path: writes against the room and maxlength, free_cb, and the cork and flush that keep their order
#include "pcmframe/codec.h"
#include "pulse-shim/abi.h"
#include "support/pulse-client.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdint>
#include <cstring>
#include <mutex>
#include <string>
#include <utility>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;
using namespace cadence::test_support::pulse;

namespace {

constexpr std::size_t kScribbled = 4096;

std::mutex freedMutex;
std::vector<void*> freed;

// overwrites what it frees, so bytes the shim had not copied yet would arrive as zeros
void freeAndScribble(void* p) {
  if (p != nullptr)
    std::memset(p, 0, kScribbled);
  std::scoped_lock lock(freedMutex);
  freed.push_back(p);
}

std::vector<void*> freedSoFar() {
  std::scoped_lock lock(freedMutex);
  return freed;
}

int writeFreed(Client& client, pa_stream* s, const void* data, std::size_t n) {
  return client.locked([&] { return pa_stream_write(s, data, n, freeAndScribble, 0, PA_SEEK_RELATIVE); });
}

pcmframe::ToServer cork(bool on) { return pcmframe::Cork{on}; }

}  // namespace

TEST_CASE("writable_size is tlength less what waits, and grows back as the server reads") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  // 2 s of float stereo, more than the socket can hold
  const std::size_t tlength = 705600;
  const auto asked = attr(static_cast<std::uint32_t>(tlength));
  REQUIRE(client.connect(s, &asked, PA_STREAM_START_CORKED) == 0);
  client.ready(s);
  CHECK(client.writable(s) == tlength);

  const std::size_t chunk = 32 * 1024;
  Bytes written;
  while (client.writable(s) >= chunk) {
    const auto bytes = pattern(written.size(), chunk);
    REQUIRE(client.write(s, bytes) == 0);
    written.insert(written.end(), bytes.begin(), bytes.end());
    CHECK(client.writable(s) == tlength - written.size());
  }
  const auto rest = pattern(written.size(), client.writable(s));
  REQUIRE(client.write(s, rest) == 0);
  written.insert(written.end(), rest.begin(), rest.end());
  CHECK(client.writable(s) == 0);

  client.cork(s, false, seen);
  // the sender moves bytes into the socket, which holds far less than tlength while the server reads nothing
  REQUIRE(eventually([&] { return client.writable(s) > 0; }));
  CHECK(client.writable(s) < tlength);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  CHECK(server.next() == cork(true));
  CHECK(server.next() == cork(false));
  CHECK(server.pcm(written.size()) == written);
  CHECK(eventually([&] { return client.writable(s) == tlength; }));
  client.finish(s);
}

TEST_CASE("a write past the room within maxlength is accepted, and one past maxlength is invalid") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  // a tlength below the 32 KiB a client writes at once; no maxlength: tlength + 64 KiB
  const auto asked = attr(4096);
  REQUIRE(client.connect(s, &asked, PA_STREAM_START_CORKED) == 0);
  client.ready(s);
  CHECK(client.writable(s) == 4096);

  const auto bytes = pattern(0, 64 * 1024);
  REQUIRE(client.write(s, Bytes(bytes.begin(), bytes.begin() + 32 * 1024)) == 0);
  CHECK(client.writable(s) == 0);
  REQUIRE(client.write(s, Bytes(bytes.begin() + 32 * 1024, bytes.end())) == 0);
  CHECK(client.write(s, pattern(0, 4096 + 8)) < 0);
  CHECK(client.error() == PA_ERR_INVALID);
  // exactly maxlength
  REQUIRE(client.write(s, pattern(65536, 4096)) == 0);
  CHECK(client.write(s, Bytes(8, 0)) < 0);

  client.cork(s, false, seen);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  CHECK(server.next() == cork(true));
  CHECK(server.next() == cork(false));
  CHECK(server.pcm(bytes.size()) == bytes);
  CHECK(server.pcm(4096) == pattern(65536, 4096));
  CHECK(eventually([&] { return client.writable(s) == 4096; }));
  client.finish(s);
}

TEST_CASE("a write past an asked maxlength fails invalid and sends nothing") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  const auto asked = attr(4096, 8192);
  REQUIRE(client.connect(s, &asked, PA_STREAM_NOFLAGS) == 0);
  client.ready(s);
  CHECK(client.writable(s) == 4096);

  const auto tooMuch = pattern(0, 8192 + 8);
  CHECK(client.write(s, tooMuch) < 0);
  CHECK(client.error() == PA_ERR_INVALID);
  CHECK(client.writable(s) == 4096);
  // a flush is the next thing the server sees, so the refused bytes never went out
  client.flush(s, seen);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Flush{}});

  const auto bytes = pattern(0, 4096);
  REQUIRE(client.write(s, bytes) == 0);
  CHECK(server.pcm(4096) == bytes);
  CHECK(client.write(s, tooMuch) < 0);
  CHECK(client.error() == PA_ERR_INVALID);
  client.cork(s, true, seen);
  CHECK(server.next() == cork(true));
  client.finish(s);
}

TEST_CASE("only appending writes are supported, on a ready stream") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  CHECK(client.write(s, pattern(0, 8)) < 0);
  CHECK(client.error() == PA_ERR_BADSTATE);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(s);
  const auto bytes = pattern(0, 8);
  CHECK(client.locked([&] { return pa_stream_write(s, bytes.data(), 8, nullptr, 8, PA_SEEK_RELATIVE); }) < 0);
  CHECK(client.error() == PA_ERR_NOTSUPPORTED);
  CHECK(client.locked([&] { return pa_stream_write(s, bytes.data(), 8, nullptr, 0, 1); }) < 0);
  CHECK(client.error() == PA_ERR_NOTSUPPORTED);
  CHECK(client.locked([&] { return pa_stream_write(s, nullptr, 8, nullptr, 0, PA_SEEK_RELATIVE); }) < 0);
  CHECK(client.error() == PA_ERR_INVALID);
  client.finish(s);
}

TEST_CASE("free_cb runs once, after the copy") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_NOFLAGS) == 0);
  client.ready(s);

  auto data = pattern(0, kScribbled);
  const auto original = data;
  REQUIRE(writeFreed(client, s, data.data(), data.size()) == 0);
  CHECK(freedSoFar() == std::vector<void*>{data.data()});
  CHECK(data == Bytes(kScribbled, 0));
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  CHECK(server.pcm(kScribbled) == original);

  // a refused write leaves its data with the caller: past the default maxlength, 4 x tlength
  const auto tooMuch = pattern(0, 4 * kDefaultTlength + 8);
  CHECK(writeFreed(client, s, tooMuch.data(), tooMuch.size()) < 0);
  CHECK(freedSoFar().size() == 1);
  // and an empty write with no data frees nothing
  CHECK(writeFreed(client, s, nullptr, 0) == 0);
  CHECK(freedSoFar().size() == 1);
  client.finish(s);
}

TEST_CASE("cork and uncork are forwarded and is_corked follows") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  CHECK(client.corked(s) == 0);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_START_CORKED) == 0);
  client.ready(s);
  CHECK(client.corked(s) == 1);
  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  CHECK(server.next() == cork(true));

  client.cork(s, false, seen);
  CHECK(client.corked(s) == 0);
  CHECK(server.next() == cork(false));
  client.cork(s, true, seen);
  CHECK(client.corked(s) == 1);
  CHECK(server.next() == cork(true));
  // a cork that changes nothing sends nothing: the flush after it comes next
  client.cork(s, true, seen);
  client.flush(s, seen);
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Flush{}});
  CHECK(seen.copy(seen.successes) == std::vector<std::pair<int, std::string>>(4, {1, kLoopThread}));
  client.finish(s);
}

TEST_CASE("a flush sends flush, and no byte written before it arrives after it") {
  Listener listener;
  Seen seen;
  Client client(listener);
  auto server = listener.accept();
  auto* s = client.stream(seen, PA_SAMPLE_FLOAT32LE, 44100, 2);
  REQUIRE(client.connect(s, nullptr, PA_STREAM_START_CORKED) == 0);
  client.ready(s);

  REQUIRE(client.write(s, Bytes(4096, 0xaa)) == 0);
  CHECK(client.writable(s) == kDefaultTlength - 4096);
  client.flush(s, seen);
  CHECK(client.writable(s) == kDefaultTlength);
  REQUIRE(client.write(s, Bytes(4096, 0xbb)) == 0);
  client.cork(s, false, seen);

  CHECK(server.next() == pcmframe::ToServer{floatStereo()});
  CHECK(server.next() == cork(true));
  CHECK(server.next() == pcmframe::ToServer{pcmframe::Flush{}});
  CHECK(server.next() == cork(false));
  CHECK(server.pcm(4096) == Bytes(4096, 0xbb));
  CHECK(seen.copy(seen.successes) == std::vector<std::pair<int, std::string>>(2, {1, kLoopThread}));
  client.finish(s);
}
