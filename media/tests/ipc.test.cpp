#include "audiod/daemon.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/soloist-jail.h"
#include "audiod/sys.h"
#include "support/audiod-child.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"

#ifdef CADENCE_MEDIA_AIRPLAY
#include "audiod/airplay-stack.h"
#endif

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

#include <fcntl.h>
#include <signal.h>
#include <spawn.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <unistd.h>

#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <iterator>
#include <memory>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

// a sim build leaves avahi and nqptp out (M4d-D8); a device build, or one without airplay, never does
#ifdef CADENCE_MEDIA_AIRPLAY
constexpr bool kLanLeftOut = !kAirplayLan;
#else
constexpr bool kLanLeftOut = false;
#endif

constexpr auto kFile = cadence_audiod_v1_Backend_BACKEND_FILE;
constexpr auto kAlsa = cadence_audiod_v1_Backend_BACKEND_ALSA;
constexpr auto kUnspecified = cadence_audiod_v1_Backend_BACKEND_UNSPECIFIED;

// what a crashed daemon leaves at its path
void leaveStaleSocket(const std::string& path) {
  const UniqueFd fd(::socket(AF_UNIX, SOCK_SEQPACKET, 0));
  REQUIRE(fd);
  sockaddr_un address{};
  address.sun_family = AF_UNIX;
  std::memcpy(address.sun_path, path.data(), path.size());
  REQUIRE(::bind(fd.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
}

std::string event(std::string_view msg) { return "\"msg\":\"" + std::string(msg) + "\""; }

void appendVarint(std::vector<std::uint8_t>& out, std::size_t value) {
  for (; value >= 0x80; value >>= 7)
    out.push_back(static_cast<std::uint8_t>(value | 0x80));
  out.push_back(static_cast<std::uint8_t>(value));
}

// a length-delimited protobuf field, built by hand so it can exceed audiod's own bounds
std::vector<std::uint8_t> field(std::uint32_t number, const std::vector<std::uint8_t>& payload) {
  std::vector<std::uint8_t> out;
  appendVarint(out, (number << 3) | 2);
  appendVarint(out, payload.size());
  out.insert(out.end(), payload.begin(), payload.end());
  return out;
}

std::vector<std::uint8_t> audioDownWithOpus(std::size_t bytes) {
  auto body = field(1, {'s'});
  const auto opus = field(3, std::vector<std::uint8_t>(bytes, 0x5a));
  body.insert(body.end(), opus.begin(), opus.end());
  return field(cadence_audiod_v1_Envelope_audio_down_tag, body);
}

std::vector<std::uint8_t> streamStartWithId(std::size_t bytes) {
  return field(cadence_audiod_v1_Envelope_stream_start_tag, field(1, std::vector<std::uint8_t>(bytes, 'a')));
}

// notes when it goes
class Recorder final : public Part {
 public:
  Recorder(std::vector<int>& gone, int id) : gone_(gone), id_(id) {}
  ~Recorder() override { gone_.push_back(id_); }
  void configure(const Configure& /*configuration*/) override {}
  void reset() override {}

 private:
  std::vector<int>& gone_;
  int id_;
};

}  // namespace

TEST_CASE("PingPong") {
  AudiodHarness audiod;
  const auto client = connectTo(audiod.socket());

  sendFrame(client.get(), ping(42));

  const auto pong = expectFrame(client.get(), cadence_audiod_v1_Envelope_pong_tag).body.pong;
  CHECK(pong.seq == 42);
  CHECK(std::string(pong.version) == kVersion);
}

TEST_CASE("ConfigureAnswersReady") {
  AudiodHarness audiod({kFile});
  const auto client = connectTo(audiod.socket());

  sendFrame(client.get(), configure(kFile, kFile));
  const auto ready = expectReady(client.get());
  CHECK(std::string(ready.version) == kVersion);
  REQUIRE(ready.backends_count == 1);
  CHECK(ready.backends[0] == kFile);

  SECTION("a second configure replaces the first") {
    auto outputOnly = configure(kFile, kFile);
    outputOnly.body.configure.has_capture = false;
    sendFrame(client.get(), outputOnly);
    expectReady(client.get());

    const auto hasCapture = audiod.onLoop([](IpcServer&, Daemon& daemon) {
      return daemon.configuration().has_value() && daemon.configuration()->has_capture;
    });
    CHECK_FALSE(hasCapture);
    CHECK(audiod.logs().count(event("configured")) == 2);
  }
}

TEST_CASE("ClientGoneForgetsConfiguration") {
  AudiodHarness audiod({kFile});
  const auto client = connectTo(audiod.socket());
  sendFrame(client.get(), configure(kFile, kFile));
  expectReady(client.get());
  const auto configured = [&] {
    return audiod.onLoop([](IpcServer&, Daemon& daemon) { return daemon.configuration().has_value(); });
  };
  REQUIRE(configured());

  SECTION("the client closes") {
    ::shutdown(client.get(), SHUT_RDWR);
    REQUIRE(audiod.logs().eventually(event("ipc_client_gone")));
  }
  SECTION("the client sends garbage") {
    sendBytes(client.get(), std::vector<std::uint8_t>(6, 0xff));
    REQUIRE(audiod.logs().eventually(event("ipc_invalid")));
  }
  SECTION("a reply cannot reach the client, which is then dropped through its end of stream") {
    ::shutdown(client.get(), SHUT_RD);
    sendFrame(client.get(), ping(1));
    REQUIRE(audiod.logs().eventually(event("ipc_client_gone")));
    CHECK(audiod.logs().count(event("ipc_send_failed")) == 1);
  }

  CHECK(eventually([&] { return !configured(); }));
  expectPong(connectTo(audiod.socket()).get(), 2);
}

// a part may use the ones added before it
TEST_CASE("DaemonDestroysPartsInReverse") {
  TempDir dir;
  const Log log([](std::string_view) {});
  const auto loop = Loop::create(log);
  REQUIRE(loop);
  const auto ipc = IpcServer::listen(*loop, log, dir.file("audiod.sock"));
  REQUIRE(ipc);
  std::vector<int> gone;
  {
    Daemon daemon(*ipc, log, {kFile});
    for (int id = 1; id <= 3; ++id)
      daemon.add(std::make_unique<Recorder>(gone, id));
  }
  CHECK(gone == std::vector{3, 2, 1});
}

TEST_CASE("ConfigureRefusesUnknownBackend") {
  // alsa is a known backend but not in this daemon's build
  const auto [capture, output] = GENERATE(std::pair{kAlsa, kFile}, std::pair{kFile, kUnspecified},
                                          std::pair{static_cast<cadence_audiod_v1_Backend>(3), kFile});
  AudiodHarness audiod({kFile});
  const auto client = connectTo(audiod.socket());

  sendFrame(client.get(), configure(capture, output));

  expectClosed(client.get());
  CHECK(audiod.logs().eventually(event("configure_refused")));
  CHECK_FALSE(audiod.onLoop([](IpcServer&, Daemon& daemon) { return daemon.configuration().has_value(); }));
  expectPong(connectTo(audiod.socket()).get(), 1);
}

TEST_CASE("RejectsOversizeFrame") {
  AudiodHarness audiod;
  const auto client = connectTo(audiod.socket());

  sendBytes(client.get(), paddedPing(kMaxFrameBytes));
  CHECK(expectFrame(client.get(), cadence_audiod_v1_Envelope_pong_tag).body.pong.seq == 1);

  SECTION("one byte over, as a valid envelope, so only its size is wrong") {
    sendBytes(client.get(), paddedPing(kMaxFrameBytes + 1));
  }
  SECTION("far over the receive buffer") { sendBytes(client.get(), std::vector<std::uint8_t>(65536, 0)); }

  expectClosed(client.get());
  CHECK(audiod.logs().eventually(event("ipc_invalid") + R"(,"reason":"oversize")"));
  expectPong(connectTo(audiod.socket()).get(), 2);
}

TEST_CASE("RejectsGarbage") {
  AudiodHarness audiod;
  const auto client = connectTo(audiod.socket());

  sendBytes(client.get(), std::vector<std::uint8_t>(6, 0xff));

  expectClosed(client.get());
  CHECK(audiod.logs().eventually(event("ipc_invalid") + R"(,"reason":"undecodable")"));
  expectPong(connectTo(audiod.socket()).get(), 1);
}

// audiod.options bounds fields tighter than the frame: one over its bound makes the envelope undecodable, and only
// the client goes
TEST_CASE("RejectsOverLimitField") {
  AudiodHarness audiod;
  const auto client = connectTo(audiod.socket());

  // at the bounds the client stays: the packet of no stream is ignored, the ungated stream refused
  sendBytes(client.get(), audioDownWithOpus(4000));
  sendBytes(client.get(), streamStartWithId(127));
  const auto done = expectFrame(client.get(), cadence_audiod_v1_Envelope_stream_done_tag).body.stream_done;
  CHECK(std::string_view(done.stream_id) == std::string(127, 'a'));
  expectPong(client.get(), 1);

  SECTION("opus over 4000 bytes") { sendBytes(client.get(), audioDownWithOpus(4001)); }
  SECTION("a stream id over 127 bytes") { sendBytes(client.get(), streamStartWithId(128)); }

  expectClosed(client.get());
  CHECK(audiod.logs().eventually(event("ipc_invalid") + R"(,"reason":"undecodable")"));
  expectPong(connectTo(audiod.socket()).get(), 2);
}

TEST_CASE("UnhandledBodiesAreSkipped") {
  AudiodHarness audiod;
  const auto client = connectTo(audiod.socket());

  sendBytes(client.get(), unknownBody());
  // ready is audiod's own message, never one it handles
  Envelope ready = cadence_audiod_v1_Envelope_init_zero;
  ready.which_body = cadence_audiod_v1_Envelope_ready_tag;
  sendFrame(client.get(), ready);

  expectPong(client.get(), 3);
  CHECK(audiod.logs().count(event("ipc_unhandled") + R"(,"type":0)") == 1);
  CHECK(audiod.logs().count(event("ipc_unhandled") + R"(,"type":4)") == 1);
}

TEST_CASE("SecondClientRefused") {
  AudiodHarness audiod;
  const auto first = connectTo(audiod.socket());
  expectPong(first.get(), 1);

  const auto second = connectTo(audiod.socket());

  expectClosed(second.get());
  CHECK(audiod.logs().eventually(event("ipc_client_refused")));
  expectPong(first.get(), 2);

  SECTION("the next client is taken once the first left") {
    ::shutdown(first.get(), SHUT_RDWR);
    REQUIRE(audiod.logs().eventually(event("ipc_client_gone")));
    expectPong(connectTo(audiod.socket()).get(), 3);
  }
}

TEST_CASE("SocketIsPrivate") {
  AudiodHarness audiod;

  struct stat info {};
  REQUIRE(::stat(audiod.socket().c_str(), &info) == 0);
  CHECK(S_ISSOCK(info.st_mode));
  CHECK((info.st_mode & 0777) == 0600);
}

// android: the app's processes share no directory, so cadenced dials an abstract name, which leaves no file behind
TEST_CASE("ListensOnAnAbstractName") {
  const std::string name = "@cadence-ipc-test-" + std::to_string(::getpid());
  {
    AudiodHarness audiod(compiledBackends(), name);
    expectPong(connectTo(name).get(), 1);
  }
  CHECK_FALSE(tryConnect(name));
}

TEST_CASE("ReplacesStaleSocket") {
  TempDir dir;
  const auto path = dir.file("audiod.sock");
  leaveStaleSocket(path);

  AudiodHarness audiod(compiledBackends(), path);

  expectPong(connectTo(path).get(), 1);
}

TEST_CASE("RefusesPathThatIsNoSocket") {
  TempDir dir;
  const auto path = dir.file("audiod.sock");
  std::ofstream(path) << "keep";
  LogCapture logs;
  const auto log = logs.log();
  auto loop = Loop::create(log);
  REQUIRE(loop);

  CHECK(IpcServer::listen(*loop, log, path) == nullptr);

  CHECK(logs.count(event("ipc_listen_failed") + R"(,"reason":"not_socket")") == 1);
  std::ifstream kept(path);
  CHECK(std::string(std::istreambuf_iterator<char>(kept), {}) == "keep");
}

TEST_CASE("SendDropsWhileClientStalls") {
  AudiodHarness audiod;
  const auto client = connectTo(audiod.socket());
  expectPong(client.get(), 1);
  const auto flood = [](IpcServer& ipc, Daemon&) {
    Envelope pong = cadence_audiod_v1_Envelope_init_zero;
    pong.which_body = cadence_audiod_v1_Envelope_pong_tag;
    int dropped = 0;
    for (int i = 0; i < 20000; ++i)
      dropped += ipc.send(pong) ? 0 : 1;
    return dropped;
  };

  CHECK(audiod.onLoop(flood) > 0);
  // one line for the whole stall, and the client stays
  CHECK(audiod.logs().count(event("ipc_send_dropped")) == 1);
  CHECK(audiod.onLoop([](IpcServer& ipc, Daemon&) { return ipc.hasClient(); }));

  SECTION("a later stall is logged again") {
    while (receive(client.get(), 50ms).kind == Received::Kind::frame) {
    }
    expectPong(client.get(), 2);
    CHECK(audiod.onLoop(flood) > 0);
    CHECK(audiod.logs().count(event("ipc_send_dropped")) == 2);
  }
}

TEST_CASE("ExitsOnSigterm") {
  const int signo = GENERATE(SIGTERM, SIGINT);
  TempDir dir;
  const auto socket = dir.file("audiod.sock");
  Child audiod({"--socket", socket});
  UniqueFd client;
  REQUIRE(eventually([&] {
    client = tryConnect(socket);
    return client.valid();
  }));
  sendFrame(client.get(), configure(kFile, kFile));
  expectReady(client.get());
  expectPong(client.get(), 1);

  audiod.signal(signo);

  const int status = audiod.wait();
  CHECK(WIFEXITED(status));
  CHECK(WEXITSTATUS(status) == 0);
  CHECK_FALSE(std::filesystem::exists(socket));
  expectClosed(client.get());
  const auto output = audiod.output();
  CHECK(output.find(event("audiod_start")) != std::string::npos);
  CHECK(output.find(event("audiod_stop") + ",\"signal\":" + std::to_string(signo)) != std::string::npos);
  // only a sim build runs soloist unjailed, and says so once
  const auto unjailed = output.find(event("soloist_unjailed"));
  CHECK((unjailed != std::string::npos) == !kSoloistJailed);
  CHECK(output.find(event("soloist_unjailed"), unjailed + 1) == std::string::npos);
  // and leaves airplay's lan daemons out
  const auto lanOff = output.find(event("airplay_lan_off"));
  CHECK((lanOff != std::string::npos) == kLanLeftOut);
  CHECK(output.find(event("airplay_lan_off"), lanOff + 1) == std::string::npos);
}

TEST_CASE("CommandLine") {
  SECTION("--version prints the version") {
    Child audiod({"--version"});
    const int status = audiod.wait();
    CHECK(WIFEXITED(status));
    CHECK(WEXITSTATUS(status) == 0);
    CHECK(audiod.output() == "cadence-audiod " + std::string(kVersion) + "\n");
    CHECK(kVersion.ends_with("-sim") == (!kSoloistJailed || kLanLeftOut));
  }
  SECTION("a missing socket is a usage error") {
    Child audiod({});
    const int status = audiod.wait();
    CHECK(WIFEXITED(status));
    CHECK(WEXITSTATUS(status) == 2);
  }
}
