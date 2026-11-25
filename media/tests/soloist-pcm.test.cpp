#include "audiod/soloist-pcm.h"
#include "backend/pcm-input.h"
#include "pcmframe/codec.h"
#include "support/audiod-harness.h"
#include "support/backend-player.h"
#include "support/eventually.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <signal.h>
#include <spawn.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <unistd.h>

#include <atomic>
#include <chrono>
#include <cmath>
#include <cstdint>
#include <cstring>
#include <fstream>
#include <future>
#include <numbers>
#include <optional>
#include <span>
#include <string>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using Catch::Matchers::WithinAbs;
using namespace std::chrono_literals;
using Bytes = std::vector<std::uint8_t>;
using Clock = std::chrono::steady_clock;

namespace {

constexpr pcmframe::Format kS16Stereo44{pcmframe::Encoding::S16Le, 2, 44100};
constexpr pcmframe::Format kS16Stereo48{pcmframe::Encoding::S16Le, 2, 48000};
constexpr pcmframe::Format kF32Stereo48{pcmframe::Encoding::F32Le, 2, 48000};

// frames [first, first + frames) of a 1 kHz sine at half scale, interleaved stereo s16 at rate
Bytes tone(std::size_t first, std::size_t frames, double rate) {
  Bytes bytes;
  for (std::size_t i = first; i < first + frames; ++i) {
    const auto value = static_cast<std::int16_t>(
        std::lround(16384 * std::sin(2 * std::numbers::pi * 1000 * static_cast<double>(i) / rate)));
    const auto raw = static_cast<std::uint16_t>(value);
    for (int channel = 0; channel < 2; ++channel) {
      bytes.push_back(static_cast<std::uint8_t>(raw));
      bytes.push_back(static_cast<std::uint8_t>(raw >> 8));
    }
  }
  return bytes;
}

// bytes of ms of a format
std::size_t bytesOf(const pcmframe::Format& format, std::size_t ms) {
  return format.rate * ms / 1000 * format.frameBytes();
}

Bytes silence(const pcmframe::Format& format, std::size_t ms) { return Bytes(bytesOf(format, ms), 0); }

// the shim's end of the socket, as a test drives it
class Shim {
 public:
  explicit Shim(const std::string& path) {
    sockaddr_un address{};
    address.sun_family = AF_UNIX;
    std::memcpy(address.sun_path, path.c_str(), path.size() + 1);
    socket_ = UniqueFd(::socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0));
    REQUIRE(::connect(socket_.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
    const timeval patience{5, 0};
    ::setsockopt(socket_.get(), SOL_SOCKET, SO_RCVTIMEO, &patience, sizeof patience);
  }

  void send(const pcmframe::ToServer& message) {
    Bytes wire;
    REQUIRE(pcmframe::encode(message, wire));
    raw(wire);
  }

  void pcm(const Bytes& bytes) { send(pcmframe::Pcm{bytes}); }

  void raw(const Bytes& wire) const { REQUIRE(sent(wire)); }

  // as pcm, without an assertion, for a sender thread beside the test's own
  [[nodiscard]] bool tryPcm(const Bytes& bytes) const {
    Bytes wire;
    return pcmframe::encode(pcmframe::Pcm{bytes}, wire) && sent(wire);
  }

  // the next Played total; none once the server closed the connection or nothing came in 5 s
  std::optional<std::uint64_t> played() {
    for (;;) {
      const auto next = decoder_.nextToShim();
      REQUIRE(next.ok());
      if (next.value())
        return next.value()->total;
      std::uint8_t chunk[64];
      const auto n = ::recv(socket_.get(), chunk, sizeof chunk, 0);
      if (n <= 0)
        return std::nullopt;
      decoder_.push(std::span(chunk, static_cast<std::size_t>(n)));
    }
  }

  // the server hung up
  [[nodiscard]] bool closed() const {
    std::uint8_t byte = 0;
    return ::recv(socket_.get(), &byte, 1, 0) == 0;
  }

 private:
  [[nodiscard]] bool sent(const Bytes& wire) const {
    return ::send(socket_.get(), wire.data(), wire.size(), MSG_NOSIGNAL) == static_cast<ssize_t>(wire.size());
  }

  UniqueFd socket_;
  pcmframe::Decoder decoder_;
};

// a server on a temp socket over its own music input, and the lines it logged
struct Rig {
  explicit Rig(SoloistPcmOptions options = {}) : path(dir.file("pcm.sock")) {
    server = SoloistPcm::listen(log, path, options);
    REQUIRE(server);
  }

  TempDir dir;
  std::string path;
  LogCapture logs;
  Log log = logs.log();
  PcmInput music;
  std::unique_ptr<SoloistPcm> server;
};

// the 1 kHz level of one channel of the player's output, as a fraction of full scale, over 500 ms that start 100 ms
// after the tone did
double toneLevel(const std::vector<std::int16_t>& stereo, std::size_t channel) {
  std::size_t onset = 0;
  while (onset * 2 < stereo.size() && std::abs(stereo[onset * 2 + channel]) < 64)
    ++onset;
  const std::size_t first = onset + 4800;
  REQUIRE((first + 24000) * 2 <= stereo.size());
  return amplitudeAt(stereo, 2, channel, first, 24000, 1000) / 32768;
}

std::string reasonLine(std::string_view reason) {
  return R"("msg":"soloist_pcm_invalid","reason":")" + std::string(reason) + "\"";
}

// runs the pulse-tone client of the real shim and returns its exit status, or -1 when it did not exit in time
int playTone(const std::string& socket, std::size_t ms) {
  std::string binary = CADENCE_PULSE_TONE;
  std::string path = socket;
  std::string length = std::to_string(ms);
  std::vector<char*> argv{binary.data(), path.data(), length.data(), nullptr};
  pid_t pid = -1;
  REQUIRE(::posix_spawn(&pid, binary.c_str(), nullptr, nullptr, argv.data(), environ) == 0);
  int status = 0;
  if (!eventually([&] { return ::waitpid(pid, &status, WNOHANG) == pid; }, 15s)) {
    ::kill(pid, SIGKILL);
    ::waitpid(pid, nullptr, 0);
    return -1;
  }
  return WIFEXITED(status) ? WEXITSTATUS(status) : -1;
}

}  // namespace

TEST_CASE("a 1 kHz tone at 44.1 kHz s16 stereo plays resampled at unity gain, each pcm frame answered with the total") {
  Rig rig;
  rig.server->openOutput(rig.music);
  BackendPlayer player(rig.music);
  Shim shim(rig.path);
  shim.send(kS16Stereo44);
  std::uint64_t total = 0;
  // 1 s in 100 ms frames
  for (std::size_t first = 0; first < 44100; first += 4410) {
    const auto bytes = tone(first, 4410, 44100);
    shim.pcm(bytes);
    total += bytes.size();
    CHECK(shim.played() == total);
  }
  REQUIRE(eventually([&] { return rig.music.status().queued == 0; }));
  std::this_thread::sleep_for(100ms);
  player.stop();
  const auto out = player.output();
  CHECK_THAT(db(toneLevel(out, 0) / 0.5), WithinAbs(0, 0.5));
  CHECK_THAT(db(toneLevel(out, 1) / 0.5), WithinAbs(0, 0.5));
  CHECK(rig.logs.count("soloist_pcm_connected") == 1);
  CHECK(rig.logs.count(R"("msg":"soloist_pcm_format","encoding":"s16","channels":2,"rate":44100)") == 1);
}

TEST_CASE("a push waits for the backend: the input never holds more than the target and a slice, acks follow reads") {
  Rig rig;
  rig.server->openOutput(rig.music);
  BackendPlayer player(rig.music);
  Shim shim(rig.path);
  shim.send(kF32Stereo48);
  std::atomic<bool> watching{true};
  std::atomic<std::uint64_t> most{0};
  std::thread watcher([&] {
    while (watching) {
      const auto queued = rig.music.status().queued;
      if (queued > most)
        most = queued;
      std::this_thread::sleep_for(1ms);
    }
  });
  // 1 s in 100 ms frames, sent as fast as the socket takes them
  const auto frame = silence(kF32Stereo48, 100);
  const auto begin = Clock::now();
  std::atomic<bool> sent{true};
  std::thread sender([&] {
    for (int i = 0; i < 10; ++i)
      sent = sent && shim.tryPcm(Bytes(frame.size(), 1));
  });
  std::vector<Clock::duration> acked;
  for (int i = 0; i < 10; ++i) {
    REQUIRE(shim.played() == frame.size() * static_cast<std::size_t>(i + 1));
    acked.push_back(Clock::now() - begin);
  }
  sender.join();
  watching = false;
  watcher.join();
  CHECK(sent);
  // 150 ms and one 20 ms slice at 48 kHz
  CHECK(most <= 7200 + 960);
  // a frame is answered once its last slice was queued, and a slice is queued once the backend left room: the n-th,
  // n x 100 ms in, comes no sooner than what was read by then plus the target and a slice (and a period)
  for (std::size_t n = 0; n < acked.size(); ++n)
    CHECK(acked[n] >= std::chrono::milliseconds(100 * static_cast<long>(n + 1)) - 200ms);
}

TEST_CASE("a cork and a flush clear what the input holds") {
  Rig rig;
  rig.server->openOutput(rig.music);
  Shim shim(rig.path);
  shim.send(kS16Stereo48);
  shim.pcm(silence(kS16Stereo48, 100));
  REQUIRE(shim.played());
  CHECK(rig.music.status().queued == 4800);

  // an empty pcm frame is answered too, after what came before it
  shim.send(pcmframe::Cork{true});
  shim.pcm({});
  REQUIRE(shim.played());
  CHECK(rig.music.status().queued == 0);

  shim.send(pcmframe::Cork{false});
  shim.pcm(silence(kS16Stereo48, 50));
  REQUIRE(shim.played());
  CHECK(rig.music.status().queued == 2400);
  shim.send(pcmframe::Flush{});
  shim.pcm({});
  REQUIRE(shim.played());
  CHECK(rig.music.status().queued == 0);
}

TEST_CASE("while the output is closed the frames are consumed at real-time pace, dropped and answered") {
  Rig rig;
  Shim shim(rig.path);
  shim.send(kF32Stereo48);
  const auto frame = silence(kF32Stereo48, 50);
  const auto begin = Clock::now();
  std::atomic<bool> sent{true};
  std::thread sender([&] {
    for (int i = 0; i < 10; ++i)
      sent = sent && shim.tryPcm(frame);
  });
  std::uint64_t last = 0;
  for (int i = 0; i < 10; ++i) {
    const auto total = shim.played();
    REQUIRE(total);
    last = *total;
  }
  const auto elapsed = Clock::now() - begin;
  sender.join();
  CHECK(sent);
  CHECK(last == 10 * frame.size());
  CHECK(elapsed >= 450ms);
  CHECK(elapsed < 1500ms);
  CHECK(rig.music.status().framesIn == 0);
}

TEST_CASE("closing the output clears the input and stops the pushes; opening it again resumes them") {
  Rig rig;
  rig.server->openOutput(rig.music);
  Shim shim(rig.path);
  shim.send(kS16Stereo48);
  shim.pcm(silence(kS16Stereo48, 100));
  REQUIRE(shim.played());
  CHECK(rig.music.status().queued == 4800);

  rig.server->closeOutput();
  CHECK(rig.music.status().queued == 0);
  shim.pcm(silence(kS16Stereo48, 20));
  REQUIRE(shim.played());
  CHECK(rig.music.status().framesIn == 4800);

  rig.server->openOutput(rig.music);
  shim.pcm(silence(kS16Stereo48, 20));
  REQUIRE(shim.played());
  CHECK(rig.music.status().framesIn == 4800 + 960);
}

// multiroom (P1): a group leader's retarget closes the output on the music input and opens it on its feed, and back
TEST_CASE("the frames go into the input the output was opened on, and follow a reopen on another") {
  Rig rig;
  PcmInput feed;
  rig.server->openOutput(rig.music);
  Shim shim(rig.path);
  shim.send(kS16Stereo48);
  shim.pcm(silence(kS16Stereo48, 20));
  REQUIRE(shim.played());
  CHECK(rig.music.status().framesIn == 960);

  rig.server->closeOutput();
  rig.server->openOutput(feed);
  shim.pcm(silence(kS16Stereo48, 40));
  REQUIRE(shim.played());
  CHECK(feed.status().framesIn == 1920);
  CHECK(rig.music.status().framesIn == 960);
  CHECK(rig.music.status().queued == 0);

  // an open on another input while one is open closes that one first: what it held is cleared
  rig.server->openOutput(rig.music);
  CHECK(feed.status().queued == 0);
  shim.pcm(silence(kS16Stereo48, 20));
  REQUIRE(shim.played());
  CHECK(rig.music.status().framesIn == 1920);
  CHECK(feed.status().framesIn == 1920);
}

TEST_CASE("a retarget while soloist streams moves its audio without a stuck push, and back") {
  Rig rig;
  PcmInput feed;
  rig.server->openOutput(rig.music);
  BackendPlayer music(rig.music);
  Shim shim(rig.path);
  shim.send(kS16Stereo48);
  std::atomic<bool> streaming{true};
  std::atomic<std::uint64_t> answered{0};
  std::thread sender([&] {
    while (streaming) {
      shim.pcm(silence(kS16Stereo48, 20));
      if (!shim.played())
        return;
      ++answered;
    }
  });
  REQUIRE(eventually([&] { return rig.music.status().framesIn >= 9600; }));

  {
    BackendPlayer group(feed);
    // as MediaSessions::retarget does: closed on the music input, opened on the feed; each returns while a push waits
    auto moved = std::async(std::launch::async, [&] {
      rig.server->closeOutput();
      rig.server->openOutput(feed);
    });
    REQUIRE(moved.wait_for(2s) == std::future_status::ready);
    CHECK(rig.music.status().queued == 0);
    const auto heard = rig.music.status().framesIn;
    CHECK(eventually([&] { return feed.status().framesIn >= 9600; }));
    // nothing more reaches the music input
    CHECK(rig.music.status().framesIn == heard);

    auto back = std::async(std::launch::async, [&] {
      rig.server->closeOutput();
      rig.server->openOutput(rig.music);
    });
    REQUIRE(back.wait_for(2s) == std::future_status::ready);
    CHECK(feed.status().queued == 0);
    const auto fed = feed.status().framesIn;
    CHECK(eventually([&] { return rig.music.status().framesIn >= heard + 9600; }));
    CHECK(feed.status().framesIn == fed);
    group.stop();
  }
  const auto before = answered.load();
  CHECK(eventually([&] { return answered.load() > before + 5; }));
  streaming = false;
  sender.join();
  music.stop();
}

TEST_CASE("with nothing reading the input a slice waits out the stall bound once, then frames go at real-time pace") {
  Rig rig(SoloistPcmOptions{.queueTarget = 150ms, .stallBound = 500ms});
  rig.server->openOutput(rig.music);
  Shim shim(rig.path);
  shim.send(kS16Stereo48);
  const auto timed = [&](std::size_t frames) {
    const auto begin = Clock::now();
    for (std::size_t i = 0; i < frames; ++i) {
      shim.pcm(silence(kS16Stereo48, 100));
      REQUIRE(shim.played());
    }
    return Clock::now() - begin;
  };
  // eight slices fit below the target; the ninth, in the second frame, waits out the bound, the rest of that frame
  // (40 ms) goes unheard at real-time pace
  CHECK(timed(1) < 100ms);
  const auto stalled = timed(1);
  CHECK(stalled >= 520ms);
  CHECK(stalled < 1000ms);
  CHECK(rig.music.status().framesIn == 8 * 960);
  // nothing read since: no slice waits for the bound again, the frames are consumed at real-time pace
  const auto unheard = timed(2);
  CHECK(unheard >= 180ms);
  CHECK(unheard < 400ms);
  CHECK(rig.music.status().framesIn == 8 * 960);

  // a flush gives room, which fills again to the target and a slice, then still no wait
  shim.send(pcmframe::Flush{});
  const auto refilled = timed(3);
  CHECK(refilled < 500ms);
  CHECK(rig.music.status().framesIn == 16 * 960);

  // once the input is read the frames go into it again
  BackendPlayer player(rig.music);
  const auto before = rig.music.status().framesIn;
  for (int i = 0; i < 10 && rig.music.status().framesIn == before; ++i)
    timed(1);
  CHECK(rig.music.status().framesIn > before);
}

TEST_CASE("a malformed frame closes the connection with its reason, and the next connection plays") {
  struct Case {
    const char* reason;
    Bytes wire;
  };
  Bytes ragged;
  REQUIRE(pcmframe::encode(kS16Stereo48, ragged));
  REQUIRE(pcmframe::encode(pcmframe::Pcm{Bytes{1, 2, 3}}, ragged));
  Bytes noFormat;
  REQUIRE(pcmframe::encode(pcmframe::Pcm{Bytes(4, 0)}, noFormat));
  const std::vector<Case> cases{
      {"frame", {9}},
      {"frame", {1, 6, 0, 0, 0, 2, 3, 0x44, 0xac, 0, 0}},
      {"frame", {2, 1, 0, 1, 0}},
      {"frame", {0x81, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
      {"no_format", noFormat},
      {"ragged", ragged},
  };
  Rig rig;
  for (const auto& [reason, wire] : cases) {
    INFO(reason);
    const auto before = rig.logs.count(reasonLine(reason));
    Shim bad(rig.path);
    bad.raw(wire);
    CHECK(bad.closed());
    CHECK(rig.logs.eventually(reasonLine(reason)));
    CHECK(rig.logs.count(reasonLine(reason)) == before + 1);
  }
  Shim good(rig.path);
  good.send(kS16Stereo48);
  good.pcm(silence(kS16Stereo48, 10));
  CHECK(good.played() == bytesOf(kS16Stereo48, 10));
}

TEST_CASE("a dropped connection ends its episode: the next one needs its own format and counts its own total") {
  Rig rig;
  {
    Shim first(rig.path);
    first.send(kS16Stereo48);
    first.pcm(silence(kS16Stereo48, 100));
    CHECK(first.played() == 19200);
  }
  CHECK(rig.logs.eventually(R"("msg":"soloist_pcm_closed","bytes":19200)"));
  {
    Shim second(rig.path);
    second.pcm(silence(kS16Stereo48, 10));
    CHECK(second.closed());
  }
  Shim third(rig.path);
  third.send(kS16Stereo48);
  third.pcm(silence(kS16Stereo48, 10));
  CHECK(third.played() == 1920);
  CHECK(rig.logs.count("soloist_pcm_connected") == 3);
}

TEST_CASE("one connection at a time: a second waits until the first is gone") {
  Rig rig;
  auto first = std::make_unique<Shim>(rig.path);
  first->send(kS16Stereo48);
  first->pcm(silence(kS16Stereo48, 10));
  REQUIRE(first->played());
  auto second = std::make_unique<Shim>(rig.path);
  second->send(kS16Stereo48);
  second->pcm(silence(kS16Stereo48, 10));
  std::this_thread::sleep_for(50ms);
  CHECK(rig.logs.count("soloist_pcm_connected") == 1);
  first.reset();
  CHECK(second->played() == 1920);
  CHECK(rig.logs.count("soloist_pcm_connected") == 2);
}

TEST_CASE("the socket is 0600 from the start and, as root, owned by nobody; a stale socket or a symlink is replaced") {
  TempDir dir;
  LogCapture logs;
  const auto log = logs.log();
  PcmInput music;
  const auto path = dir.file("pcm.sock");

  // a socket a dead server left
  {
    sockaddr_un address{};
    address.sun_family = AF_UNIX;
    std::memcpy(address.sun_path, path.c_str(), path.size() + 1);
    const UniqueFd stale(::socket(AF_UNIX, SOCK_STREAM, 0));
    REQUIRE(::bind(stale.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
  }
  const auto ambient = ::umask(022);
  {
    const auto server = SoloistPcm::listen(log, path);
    REQUIRE(server);
    CHECK(::umask(022) == 022);
    struct stat info {};
    REQUIRE(::lstat(path.c_str(), &info) == 0);
    CHECK(S_ISSOCK(info.st_mode));
    CHECK((info.st_mode & 0777) == 0600);
    if (::geteuid() == 0) {
      CHECK(info.st_uid == 65534);
      CHECK(info.st_gid == 65534);
    }
  }
  ::umask(ambient);
  // gone with the server
  struct stat gone {};
  CHECK(::lstat(path.c_str(), &gone) != 0);

  // a symlink is removed, never followed
  const auto target = dir.file("target");
  std::ofstream(target) << "kept";
  REQUIRE(::symlink(target.c_str(), path.c_str()) == 0);
  {
    const auto server = SoloistPcm::listen(log, path);
    REQUIRE(server);
    struct stat info {};
    REQUIRE(::lstat(path.c_str(), &info) == 0);
    CHECK(S_ISSOCK(info.st_mode));
    REQUIRE(::lstat(target.c_str(), &info) == 0);
    CHECK(S_ISREG(info.st_mode));
    std::string kept;
    std::ifstream(target) >> kept;
    CHECK(kept == "kept");
  }

  // any other file is not the server's to remove
  std::ofstream(path) << "data";
  CHECK_FALSE(SoloistPcm::listen(log, path));
  CHECK(logs.count(R"("msg":"soloist_pcm_listen_failed","reason":"not_socket")") == 1);
}

TEST_CASE("stopping is bounded while a slice waits for the input to drain") {
  auto rig = std::make_unique<Rig>(SoloistPcmOptions{.queueTarget = 150ms, .stallBound = 60s});
  rig->server->openOutput(rig->music);
  Shim shim(rig->path);
  shim.send(kS16Stereo48);
  shim.pcm(silence(kS16Stereo48, 100));
  REQUIRE(shim.played());
  // nothing reads: the second frame waits for room after its third slice
  shim.pcm(silence(kS16Stereo48, 100));
  REQUIRE(eventually([&] { return rig->music.status().framesIn == 8 * 960; }));
  std::this_thread::sleep_for(50ms);
  auto stopped = std::async(std::launch::async, [&] { rig->server.reset(); });
  CHECK(stopped.wait_for(1s) == std::future_status::ready);
  CHECK(shim.closed());
}

TEST_CASE("a client of the real shim, its sink input turned down, plays through the server at unity gain") {
  Rig rig;
  rig.server->openOutput(rig.music);
  BackendPlayer player(rig.music);
  CHECK(playTone(rig.path, 1000) == 0);
  // 1 s of s16 stereo at 44.1 kHz
  CHECK(rig.logs.eventually(R"("msg":"soloist_pcm_closed","bytes":176400)"));
  CHECK(rig.logs.count(R"("msg":"soloist_pcm_format","encoding":"s16","channels":2,"rate":44100)") == 1);
  REQUIRE(eventually([&] { return rig.music.status().queued == 0; }));
  std::this_thread::sleep_for(100ms);
  player.stop();
  const auto out = player.output();
  CHECK_THAT(db(toneLevel(out, 0) / 0.5), WithinAbs(0, 0.5));
  CHECK_THAT(db(toneLevel(out, 1) / 0.5), WithinAbs(0, 0.5));
}
