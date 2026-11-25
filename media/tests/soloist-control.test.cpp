#include "audiod/log.h"
#include "audiod/media-source.h"
#include "audiod/soloist-control.h"
#include "support/temp-dir.h"
#include "support/ws-server.h"

#include <catch2/catch_test_macros.hpp>

#include <poll.h>
#include <sys/eventfd.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cerrno>
#include <chrono>
#include <fstream>
#include <memory>
#include <optional>
#include <string>
#include <variant>
#include <vector>

using namespace cadence::audiod;
using namespace cadence::audiod::soloist;
using namespace cadence::test_support;
using namespace std::chrono_literals;
using Clock = std::chrono::steady_clock;

namespace {

// every line the connection logs, on the test's thread
struct CapturedLog {
  std::shared_ptr<std::vector<std::string>> lines = std::make_shared<std::vector<std::string>>();
  Log log{[lines = lines](std::string_view line) { lines->emplace_back(line); }};

  [[nodiscard]] std::string all() const {
    std::string joined;
    for (const auto& line : *lines)
      joined += line;
    return joined;
  }
  [[nodiscard]] std::size_t count(std::string_view text) const {
    std::size_t found = 0;
    for (const auto& line : *lines)
      if (line.find(text) != std::string::npos)
        ++found;
    return found;
  }
};

// time moves only when the test moves it
struct FakeTime {
  Clock::time_point now = Clock::time_point{} + 1000s;

  ControlOptions options() {
    ControlOptions options;
    options.now = [this] { return now; };
    return options;
  }
};

std::unique_ptr<Connection> connectTo(const WsServer& server, const Log& log, ControlOptions options = {}) {
  auto result = connect(log, server.port(), std::move(options));
  REQUIRE(result.ok());
  return std::move(result.value());
}

// drives the connection until its next event; nullopt once it closed or the limit passed
std::optional<Event> nextEvent(Connection& connection, std::chrono::milliseconds limit = 5s) {
  const auto deadline = Clock::now() + limit;
  for (;;) {
    if (auto event = connection.next())
      return event;
    if (!connection.open())
      return std::nullopt;
    const auto left = std::chrono::duration_cast<std::chrono::milliseconds>(deadline - Clock::now()).count();
    if (left <= 0)
      return std::nullopt;
    pollfd ready{connection.fd(), POLLIN, 0};
    ::poll(&ready, 1, static_cast<int>(left));
  }
}

void write(const std::string& path, std::string_view content) {
  std::ofstream file(path, std::ios::binary | std::ios::trunc);
  file << content;
}

std::vector<std::string> payloads(const WsServer& server) {
  std::vector<std::string> out;
  for (const auto& frame : server.frames())
    out.push_back(frame.payload);
  return out;
}

}  // namespace

TEST_CASE("ThePortFileIsReadAsV1ReadIt") {
  TempDir home;
  const auto port = home.file("ws.port");
  CHECK_FALSE(readPort(home.str()).has_value());
  CHECK_FALSE(readPort(home.file("missing")).has_value());
  struct Case {
    std::string content;
    std::optional<std::uint16_t> expected;
  };
  for (const auto& [content, expected] : {
           Case{"40123", 40123},
           Case{"40123\n", 40123},
           Case{" 1\r\n", 1},
           Case{"65535", 65535},
           Case{"00080", 80},
           Case{"", std::nullopt},
           Case{"\n", std::nullopt},
           Case{"0", std::nullopt},
           Case{"65536", std::nullopt},
           Case{"-1", std::nullopt},
           Case{"+80", std::nullopt},
           Case{"80 80", std::nullopt},
           Case{"port", std::nullopt},
           Case{"4012x", std::nullopt},
           Case{std::string("12\0", 3), std::nullopt},
           Case{"99999999999999999999999999", std::nullopt},
           Case{"000000000000000000000080", std::nullopt},
           // 16 bytes are read whole; anything longer is refused, whatever hides behind the spaces
           Case{"40123" + std::string(11, ' '), 40123},
           Case{"40123" + std::string(12, ' '), std::nullopt},
           Case{"40123\n" + std::string(20, ' ') + "junk", std::nullopt},
       }) {
    INFO(content);
    write(port, content);
    CHECK(readPort(home.str()) == expected);
  }
}

TEST_CASE("ThePortFileIsNeverALinkOrAFifo") {
  TempDir home;
  const auto port = home.file("ws.port");
  const auto elsewhere = home.file("elsewhere");
  write(elsewhere, "40123");
  REQUIRE(::symlink(elsewhere.c_str(), port.c_str()) == 0);
  CHECK_FALSE(readPort(home.str()).has_value());
  REQUIRE(::unlink(port.c_str()) == 0);
  // a fifo would block a plain open until a writer came
  REQUIRE(::mkfifo(port.c_str(), 0600) == 0);
  CHECK_FALSE(readPort(home.str()).has_value());
  REQUIRE(::unlink(port.c_str()) == 0);
  REQUIRE(::mkdir(port.c_str(), 0700) == 0);
  CHECK_FALSE(readPort(home.str()).has_value());
}

TEST_CASE("ClearingThePortRemovesItsFilesAndNothingElse") {
  TempDir home;
  write(home.file("ws.port"), "40123\n");
  write(home.file("ws.addr"), "127.0.0.1:40123\n");
  write(home.file("session"), "kept");
  REQUIRE(readPort(home.str()) == 40123);
  CHECK_FALSE(clearPort(home.str()).has_value());
  CHECK_FALSE(readPort(home.str()).has_value());
  CHECK(::access(home.file("ws.port").c_str(), F_OK) != 0);
  CHECK(::access(home.file("ws.addr").c_str(), F_OK) != 0);
  CHECK(::access(home.file("session").c_str(), F_OK) == 0);
  // nothing to remove is no error; a link is removed, not what it points at
  CHECK_FALSE(clearPort(home.str()).has_value());
  REQUIRE(::symlink(home.file("session").c_str(), home.file("ws.port").c_str()) == 0);
  CHECK_FALSE(clearPort(home.str()).has_value());
  CHECK(::access(home.file("session").c_str(), F_OK) == 0);
  CHECK(clearPort(home.file("missing")).has_value());
}

TEST_CASE("TheScheduleIsTheReferences") {
  CHECK(kPortTick == 100ms);
  CHECK(kTick == 1s);
  CHECK(kPortWait == 10s);
  CHECK(kAnswerWait == 30s);
  CHECK(kReconnect == 1s);
  const ControlOptions options;
  CHECK(options.controlAnswer == 2s);
}

TEST_CASE("AConnectionIsTheAnswerAndItsEventsArriveInOrder") {
  WsServer server;
  CapturedLog captured;
  auto connection = connectTo(server, captured.log);
  CHECK(connection->open());
  CHECK(captured.count("\"msg\":\"soloist_ws_connected\"") == 1);
  server.sendText(R"({"type":"auth_state","logged_in":true,"is_active":false,"device_name":"x"})");
  server.sendText(R"({"type":"device_changed","is_active":true})");
  server.sendText(R"({"type":"command_result","command":"pause"})");
  server.sendText(R"({"type":"volume_changed","volume":42})");
  CHECK(nextEvent(*connection) == Event{Auth{.loggedIn = true, .active = false}});
  CHECK(nextEvent(*connection) == Event{Device{.active = true}});
  // nothing was sent: no one's answer
  CHECK(nextEvent(*connection) == Event{Answer{.name = "pause"}});
  CHECK(nextEvent(*connection) == Event{Volume{.level = 42}});
  CHECK(connection->counts().messages == 4);
}

TEST_CASE("AFailedConnectSaysWhy") {
  CapturedLog captured;
  WsServer silent({.handshake = WsHandshake::Silent});
  ControlOptions options;
  options.client.connectTimeout = 300ms;
  const auto timedOut = connect(captured.log, silent.port(), options);
  REQUIRE_FALSE(timedOut.ok());
  CHECK(timedOut.error() == "timeout");
  WsServer wrong({.handshake = WsHandshake::BadAccept});
  const auto refused = connect(captured.log, wrong.port());
  REQUIRE_FALSE(refused.ok());
  CHECK(refused.error() == "handshake");
  CHECK(captured.lines->empty());
}

TEST_CASE("AControlIsRefusedHereWhenNotLoggedIn") {
  WsServer server;
  CapturedLog captured;
  auto connection = connectTo(server, captured.log);
  CHECK(connection->control(Command::Pause, false) == kControlUnsupported);
  CHECK(captured.count(R"("msg":"soloist_command","command":"pause","code":"control_unsupported")") == 1);
  CHECK(connection->counts().unsupported == 1);
  CHECK(connection->counts().controls == 0);
  // a set_volume goes out behind it, so the server has seen everything sent before it
  REQUIRE(connection->setVolume(40));
  REQUIRE(server.waitFrames(1));
  CHECK(payloads(server) == std::vector<std::string>{R"({"type":"command","command":"set_volume","volume":40})"});
}

TEST_CASE("EachControlGoesOutAsItsExactCommand") {
  WsServer server;
  CapturedLog captured;
  auto connection = connectTo(server, captured.log);
  for (const auto command : {Command::Pause, Command::Play, Command::SkipNext, Command::SkipPrev})
    CHECK(connection->control(command, true) == kMediaOk);
  REQUIRE(connection->setVolume(250));
  REQUIRE(server.waitFrames(5));
  CHECK(payloads(server) == std::vector<std::string>{
                                R"({"type":"command","command":"pause"})",
                                R"({"type":"command","command":"play"})",
                                R"({"type":"command","command":"skip_next"})",
                                R"({"type":"command","command":"skip_prev"})",
                                R"({"type":"command","command":"set_volume","volume":100})",
                            });
  for (const auto name : {"pause", "play", "skip_next", "skip_prev"})
    CHECK(captured.count(std::string(R"("msg":"soloist_command","command":")") + name + R"(","code":"ok")") == 1);
  // a volume is only counted, never logged
  CHECK(captured.count("set_volume") == 0);
  CHECK(connection->counts().controls == 4);
  CHECK(connection->counts().volumes == 1);
}

TEST_CASE("AnErrorWithinTwoSecondsOfAControlIsItsAnswer") {
  WsServer server;
  CapturedLog captured;
  FakeTime time;
  auto connection = connectTo(server, captured.log, time.options());
  REQUIRE(connection->control(Command::SkipNext, true) == kMediaOk);
  time.now += 1999ms;
  server.sendText(R"({"type":"error","message":"no next track"})");
  CHECK(nextEvent(*connection) == Event{Error{.unsupported = Command::SkipNext}});
  CHECK(captured.count(R"("msg":"soloist_command","command":"skip_next","code":"control_unsupported")") == 1);
  // answered once: a second error is no one's
  server.sendText(R"({"type":"error"})");
  CHECK(nextEvent(*connection) == Event{Error{}});
  CHECK(connection->counts().errors == 2);
  CHECK(connection->counts().unsupported == 1);
}

TEST_CASE("ACommandResultWithinTwoSecondsOfAControlIsItsSuccess") {
  WsServer server;
  CapturedLog captured;
  FakeTime time;
  auto connection = connectTo(server, captured.log, time.options());
  REQUIRE(connection->control(Command::Play, true) == kMediaOk);
  time.now += 1999ms;
  server.sendText(R"({"type":"command_result","command":"play"})");
  CHECK(nextEvent(*connection) == Event{Answer{.name = "play", .control = Command::Play}});
  // answered once: an error behind it is no one's
  server.sendText(R"({"type":"error"})");
  CHECK(nextEvent(*connection) == Event{Error{}});
  CHECK(connection->counts().unsupported == 0);

  // late, or after a volume, it answers nothing
  REQUIRE(connection->control(Command::Pause, true) == kMediaOk);
  time.now += 2s;
  server.sendText(R"({"type":"command_result","command":"pause"})");
  CHECK(nextEvent(*connection) == Event{Answer{.name = "pause"}});
  REQUIRE(connection->control(Command::Pause, true) == kMediaOk);
  REQUIRE(connection->setVolume(30));
  server.sendText(R"({"type":"command_result","command":"set_volume"})");
  CHECK(nextEvent(*connection) == Event{Answer{.name = "set_volume"}});
}

TEST_CASE("ACommandResultAnswersOnlyTheCommandItNames") {
  WsServer server;
  CapturedLog captured;
  FakeTime time;
  auto connection = connectTo(server, captured.log, time.options());
  // soloist answers in order: the set_volume's result comes first, and leaves the control to its own answer
  REQUIRE(connection->setVolume(30));
  REQUIRE(connection->control(Command::SkipNext, true) == kMediaOk);
  server.sendText(R"({"type":"command_result","command":"set_volume"})");
  CHECK(nextEvent(*connection) == Event{Answer{.name = "set_volume"}});
  server.sendText(R"({"type":"error","message":"no next track"})");
  CHECK(nextEvent(*connection) == Event{Error{.unsupported = Command::SkipNext}});
  CHECK(connection->counts().unsupported == 1);

  // a result that names no command answers none
  REQUIRE(connection->control(Command::Pause, true) == kMediaOk);
  server.sendText(R"({"type":"command_result"})");
  CHECK(nextEvent(*connection) == Event{Answer{}});
  server.sendText(R"({"type":"command_result","command":"pause"})");
  CHECK(nextEvent(*connection) == Event{Answer{.name = "pause", .control = Command::Pause}});
}

TEST_CASE("AnErrorTwoSecondsAfterAControlIsNotItsAnswer") {
  WsServer server;
  CapturedLog captured;
  FakeTime time;
  auto connection = connectTo(server, captured.log, time.options());
  REQUIRE(connection->control(Command::Pause, true) == kMediaOk);
  time.now += 2s;
  server.sendText(R"({"type":"error"})");
  CHECK(nextEvent(*connection) == Event{Error{}});
  CHECK(captured.count("control_unsupported") == 0);
  CHECK(connection->counts().errors == 1);
  CHECK(connection->counts().unsupported == 0);
}

TEST_CASE("AnErrorAfterAVolumeIsOnlyCounted") {
  WsServer server;
  CapturedLog captured;
  FakeTime time;
  auto connection = connectTo(server, captured.log, time.options());
  // soloist answers in order: the error is the volume's, the latest command
  REQUIRE(connection->control(Command::Pause, true) == kMediaOk);
  REQUIRE(connection->setVolume(30));
  time.now += 100ms;
  server.sendText(R"({"type":"error"})");
  CHECK(nextEvent(*connection) == Event{Error{}});
  CHECK(captured.count("control_unsupported") == 0);
  CHECK(connection->counts().errors == 1);
  CHECK(connection->counts().unsupported == 0);
}

TEST_CASE("TheServersCloseIsLoggedOnceWithItsCode") {
  WsServer server;
  CapturedLog captured;
  auto connection = connectTo(server, captured.log);
  server.sendText(R"({"type":"volume_changed","volume":1})");
  server.sendClose(1000);
  CHECK(nextEvent(*connection) == Event{Volume{.level = 1}});
  CHECK_FALSE(nextEvent(*connection).has_value());
  CHECK_FALSE(connection->open());
  CHECK_FALSE(connection->next().has_value());
  CHECK(captured.count(R"("msg":"soloist_ws_closed","code":1000,"messages":1,"controls":0,"volumes":0,"errors":0)") == 1);
  CHECK(captured.count("soloist_ws_closed") == 1);
  // nothing goes out on a closed connection
  CHECK(connection->control(Command::Play, true) == kControlUnsupported);
  CHECK_FALSE(connection->setVolume(10));
}

TEST_CASE("ALeavingConnectionClosesGoingAway") {
  WsServer server;
  CapturedLog captured;
  auto connection = connectTo(server, captured.log);
  connection.reset();
  REQUIRE(server.waitFrames(1));
  CHECK(server.frames().at(0).opcode == 0x8);
  CHECK(server.frames().at(0).payload == std::string("\x03\xE9", 2));
  CHECK(captured.count(R"("msg":"soloist_ws_closed","code":1001)") == 1);
}

TEST_CASE("AnInvalidCloseCodeIsLoggedAsAProtocolError") {
  WsServer server;
  CapturedLog captured;
  auto connection = connectTo(server, captured.log);
  server.sendClose(1005);
  CHECK_FALSE(nextEvent(*connection).has_value());
  CHECK(captured.count(R"("msg":"soloist_ws_closed","code":1002)") == 1);
}

TEST_CASE("LeavingAPeerThatStoppedReadingIsBounded") {
  WsServer server({.reading = false});
  CapturedLog captured;
  const int stop = ::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
  REQUIRE(stop >= 0);
  ControlOptions options;
  options.client.stopFd = stop;
  std::chrono::milliseconds floor{0};
  std::chrono::milliseconds ceiling{0};
  bool stopped = false;
  SECTION("by the send timeout") {
    options.client.sendTimeout = 300ms;
    floor = 290ms;
    ceiling = 2s;
  }
  SECTION("or at once when the owner stops") {
    options.client.sendTimeout = 30s;
    ceiling = 1s;
    stopped = true;
  }
  auto connection = connectTo(server, captured.log, options);
  // the socket's buffers fill up as they would under a peer that stopped reading
  const std::string filler(65536, 'f');
  while (::send(connection->fd(), filler.data(), filler.size(), MSG_DONTWAIT | MSG_NOSIGNAL) > 0) {
  }
  REQUIRE(errno == EAGAIN);
  if (stopped) {
    const std::uint64_t one = 1;
    REQUIRE(::write(stop, &one, sizeof one) == sizeof one);
  }
  const auto start = Clock::now();
  connection.reset();
  const auto took = std::chrono::duration_cast<std::chrono::milliseconds>(Clock::now() - start);
  CHECK(took >= floor);
  CHECK(took < ceiling);
  CHECK(captured.count(R"("msg":"soloist_ws_closed","code":1001)") == 1);
  ::close(stop);
}

TEST_CASE("ASendThatStallsDropsTheConnection") {
  WsServer server({.reading = false});
  CapturedLog captured;
  ControlOptions options;
  options.client.sendTimeout = 200ms;
  auto connection = connectTo(server, captured.log, options);
  // small commands fill the socket buffers nobody reads, until one waits out the timeout
  std::size_t sent = 0;
  while (sent < 10'000'000 && connection->setVolume(50))
    ++sent;
  CHECK_FALSE(connection->open());
  CHECK(captured.count(R"("msg":"soloist_ws_closed","code":1006)") == 1);
  CHECK(connection->control(Command::Pause, true) == kControlUnsupported);
}

TEST_CASE("NoPayloadReachesTheLog") {
  WsServer server;
  CapturedLog captured;
  FakeTime time;
  auto connection = connectTo(server, captured.log, time.options());
  REQUIRE(connection->control(Command::Pause, true) == kMediaOk);
  server.sendText(R"({"type":"playback_state","status":"playing","item":{"uri":"spotify:track:2JRo0gjbX4GrCqBYdRohoo",
      "decorations":{"identity":{"name":"My Song"},"parent":{"entity":{"decorations":{"identity":{"name":"Album Name"}}}},
      "creators":[{"entity":{"decorations":{"identity":{"name":"Artist Name"}}}}],
      "visual_identity":{"cover":[{"url":"https://i.scdn.co/image/ab67616d00001e02"}]}}},"position":{"position_ms":45000}})");
  server.sendText(R"({"type":"error","message":"command requires authentication"})");
  server.sendText(R"({"type":"track_changed","item":{"decorations":{"identity":{"name":"Replacement"}}}})");
  server.sendText(R"({"type":"auth_state","logged_in":true,"is_active":true,"device_name":"Kitchen speaker"})");
  server.sendClose(1000);
  std::vector<Event> events;
  while (auto event = nextEvent(*connection))
    events.push_back(*event);
  REQUIRE(events.size() == 4);
  CHECK(std::get<Status>(events[0]).track->metadata.title == "My Song");
  const auto logged = captured.all();
  CHECK_FALSE(logged.empty());
  for (const auto secret : {"My Song", "Album Name", "Artist Name", "https", "scdn", "spotify:", "authentication",
                            "Replacement", "Kitchen", "45000"}) {
    INFO(secret);
    CHECK(logged.find(secret) == std::string::npos);
  }
}
