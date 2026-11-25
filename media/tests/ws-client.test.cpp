#include "support/eventually.h"
#include "support/ws-server.h"
#include "ws/client.h"
#include "ws/frame.h"

#include <catch2/catch_test_macros.hpp>

#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <poll.h>
#include <sys/eventfd.h>
#include <sys/ioctl.h>
#include <sys/socket.h>
#include <unistd.h>

#include <algorithm>
#include <chrono>
#include <optional>
#include <set>
#include <string>

using namespace cadence::ws;
using namespace cadence::test_support;
using namespace std::chrono_literals;
using Clock = std::chrono::steady_clock;

namespace {

Client connected(const WsServer& server, ClientOptions options = {}) {
  auto result = Client::connect(server.port(), options);
  REQUIRE(result.ok());
  return std::move(result.value());
}

std::chrono::milliseconds since(Clock::time_point start) {
  return std::chrono::duration_cast<std::chrono::milliseconds>(Clock::now() - start);
}

// drives the client until its next text message; nullopt once it closed or the limit passed
std::optional<std::string> nextText(Client& client, std::chrono::milliseconds limit = 5s) {
  const auto deadline = Clock::now() + limit;
  for (;;) {
    if (auto text = client.next())
      return text;
    if (!client.open())
      return std::nullopt;
    const auto left = std::chrono::duration_cast<std::chrono::milliseconds>(deadline - Clock::now()).count();
    if (left <= 0)
      return std::nullopt;
    pollfd ready{client.fd(), POLLIN, 0};
    ::poll(&ready, 1, static_cast<int>(left));
  }
}

// drives the client until it closed
bool closes(Client& client, std::chrono::milliseconds limit = 5s) {
  while (nextText(client, limit))
    ;
  return !client.open();
}

std::string header(const std::string& request, const std::string& name) {
  const auto at = request.find("\r\n" + name + ": ");
  if (at == std::string::npos)
    return {};
  const auto start = at + name.size() + 4;
  return request.substr(start, request.find("\r\n", start) - start);
}

std::string closePayload(std::uint16_t code) {
  return std::string{static_cast<char>(code >> 8), static_cast<char>(code & 0xFF)};
}

// a port nothing listens on
std::uint16_t closedPort() {
  const int fd = ::socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  socklen_t length = sizeof address;
  REQUIRE(::bind(fd, reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
  REQUIRE(::getsockname(fd, reinterpret_cast<sockaddr*>(&address), &length) == 0);
  ::close(fd);
  return ntohs(address.sin_port);
}

// what the kernel holds for fd to read
std::size_t queued(int fd) {
  int bytes = 0;
  return ::ioctl(fd, FIONREAD, &bytes) == 0 && bytes > 0 ? static_cast<std::size_t>(bytes) : 0;
}

}  // namespace

TEST_CASE("DefaultsAreTheReferencesBounds") {
  const ClientOptions options;
  CHECK(options.connectTimeout == 2s);
  CHECK(options.sendTimeout == 2s);
  CHECK(options.maxMessage == std::size_t{1} << 20);
  CHECK(options.stopFd == -1);
}

TEST_CASE("ConnectSendsTheOpeningRequestWithAFreshKey") {
  std::set<std::string> keys;
  for (int round = 0; round < 3; ++round) {
    WsServer server;
    auto client = connected(server);
    CHECK(client.open());
    const auto request = server.request();
    CHECK(request.starts_with("GET / HTTP/1.1\r\n"));
    CHECK(header(request, "Host") == "127.0.0.1:" + std::to_string(server.port()));
    CHECK(header(request, "Upgrade") == "websocket");
    CHECK(header(request, "Connection") == "Upgrade");
    CHECK(header(request, "Sec-WebSocket-Version") == "13");
    const auto key = header(request, "Sec-WebSocket-Key");
    // 16 bytes in base64
    CHECK(key.size() == 24);
    CHECK(key.ends_with("=="));
    keys.insert(key);
  }
  CHECK(keys.size() == 3);
}

TEST_CASE("ConnectTurnsNagleOff") {
  WsServer server;
  auto client = connected(server);
  int noDelay = 0;
  socklen_t length = sizeof noDelay;
  REQUIRE(::getsockopt(client.fd(), IPPROTO_TCP, TCP_NODELAY, &noDelay, &length) == 0);
  CHECK(noDelay == 1);
}

TEST_CASE("ConnectRefusesAWrongAcceptOrStatus") {
  for (const auto handshake : {WsHandshake::BadAccept, WsHandshake::NotSwitching}) {
    WsServer server({.handshake = handshake});
    const auto result = Client::connect(server.port());
    REQUIRE_FALSE(result.ok());
    CHECK(result.error() == ConnectError::Handshake);
    CHECK(connectErrorToken(result.error()) == "handshake");
  }
}

TEST_CASE("ConnectGivesUpOnASilentServerAfterTwoSeconds") {
  WsServer server({.handshake = WsHandshake::Silent});
  const auto start = Clock::now();
  const auto result = Client::connect(server.port());
  const auto took = since(start);
  REQUIRE_FALSE(result.ok());
  CHECK(result.error() == ConnectError::Timeout);
  CHECK(connectErrorToken(result.error()) == "timeout");
  CHECK(took >= 1990ms);
  CHECK(took < 3s);
}

TEST_CASE("ConnectBoundsTheWholeHandshakeNotEachRead") {
  // each byte comes well within the bound, the whole answer does not
  WsServer server({.handshake = WsHandshake::Trickle});
  const auto start = Clock::now();
  const auto result = Client::connect(server.port(), {.connectTimeout = 400ms});
  REQUIRE_FALSE(result.ok());
  CHECK(result.error() == ConnectError::Timeout);
  CHECK(since(start) < 1500ms);
}

TEST_CASE("ConnectFailsWithNothingListening") {
  const auto result = Client::connect(closedPort());
  REQUIRE_FALSE(result.ok());
  CHECK(result.error() == ConnectError::Connect);
  CHECK(connectErrorToken(result.error()) == "connect");
  CHECK_FALSE(Client::connect(0).ok());
}

TEST_CASE("ConnectGivesUpWhenTheOwnerStops") {
  WsServer server({.handshake = WsHandshake::Silent});
  const int stop = ::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
  REQUIRE(stop >= 0);
  const std::uint64_t one = 1;
  REQUIRE(::write(stop, &one, sizeof one) == sizeof one);
  const auto start = Clock::now();
  const auto result = Client::connect(server.port(), {.stopFd = stop});
  REQUIRE_FALSE(result.ok());
  CHECK(result.error() == ConnectError::Stopped);
  CHECK(connectErrorToken(result.error()) == "stopped");
  CHECK(since(start) < 500ms);
  ::close(stop);
}

TEST_CASE("FramesSentWithTheHandshakeAreRead") {
  // the server's first frames come in the same segment as its 101
  const std::string first = "\x81\x05" "first";
  const std::string second = "\x81\x06" "second";
  WsServer server({.afterHandshake = first + second});
  auto client = connected(server);
  CHECK(nextText(client) == "first");
  CHECK(nextText(client) == "second");
  CHECK_FALSE(client.next().has_value());
  CHECK(client.open());
}

TEST_CASE("ClientFramesAreMaskedEachWithAFreshKey") {
  WsServer server;
  auto client = connected(server);
  for (int i = 0; i < 3; ++i)
    REQUIRE(client.send("hello"));
  REQUIRE(server.waitFrames(3));
  std::set<std::array<std::uint8_t, 4>> masks;
  for (const auto& frame : server.frames()) {
    CHECK(frame.fin);
    CHECK(frame.opcode == 0x1);
    CHECK(frame.masked);
    CHECK(frame.payload == "hello");
    masks.insert(frame.mask);
  }
  CHECK(masks.size() > 1);
}

TEST_CASE("AMaskedServerFrameIsAProtocolError") {
  WsServer server;
  auto client = connected(server);
  server.sendFrame(0x81, "hi", true);
  REQUIRE(closes(client));
  CHECK(client.closeCode() == kCloseProtocolError);
  CHECK(client.fd() == -1);
  REQUIRE(server.waitFrames(1));
  const auto frame = server.frames().at(0);
  CHECK(frame.opcode == 0x8);
  CHECK(frame.masked);
  CHECK(frame.payload == closePayload(1002));
  CHECK(server.waitClientGone());
  CHECK_FALSE(client.send("after"));
}

TEST_CASE("FragmentsAreReassembledAndAPingBetweenThemAnswered") {
  WsServer server;
  auto client = connected(server);
  server.sendFrame(0x01, "Hel");
  server.sendFrame(0x89, "ping data");
  server.sendFrame(0x00, "l");
  server.sendFrame(0x80, "o");
  // the server's thread writes each frame as it comes, so a drain could end between two of them and answer the ping
  // before the message is whole: all four wait in the socket first, and one drain reads them
  constexpr std::size_t kFrameBytes = (2 + 3) + (2 + 9) + (2 + 1) + (2 + 1);
  REQUIRE(eventually([&] { return queued(client.fd()) >= kFrameBytes; }, 10s));
  CHECK(nextText(client) == "Hello");
  // the pong goes out as the drain ends
  CHECK(server.frames().empty());
  CHECK_FALSE(client.next().has_value());
  REQUIRE(server.waitFrames(1));
  const auto pong = server.frames().at(0);
  CHECK(pong.fin);
  CHECK(pong.opcode == 0xA);
  CHECK(pong.masked);
  CHECK(pong.payload == "ping data");
}

TEST_CASE("AMessageOverOneMebibyteClosesWith1009") {
  WsServer server;
  auto client = connected(server);
  const std::string whole(std::size_t{1} << 20, 'w');
  server.sendFrame(0x01, std::string_view(whole).substr(0, 1000));
  server.sendFrame(0x80, std::string_view(whole).substr(1000));
  const auto received = nextText(client);
  REQUIRE(received.has_value());
  CHECK(received->size() == whole.size());

  server.sendFrame(0x81, whole + "x");
  REQUIRE(closes(client));
  CHECK(client.closeCode() == kCloseTooBig);
  REQUIRE(server.waitFrames(1));
  CHECK(server.frames().at(0).opcode == 0x8);
  CHECK(server.frames().at(0).payload == closePayload(1009));
  CHECK(server.waitClientGone());
}

TEST_CASE("BinaryMessagesAndPongsAreIgnored") {
  WsServer server;
  auto client = connected(server);
  server.sendFrame(0x82, "\x01\x02");
  server.sendFrame(0x8A, "unasked");
  server.sendText("text");
  CHECK(nextText(client) == "text");
  CHECK(client.open());
  CHECK(server.frames().empty());
}

TEST_CASE("ACloseFromTheServerIsEchoedAndTheSocketClosed") {
  struct Case {
    std::optional<std::uint16_t> code;
    std::uint16_t closeCode;
    std::string echoed;
  };
  for (const auto& [code, closeCode, echoed] : {Case{1000, 1000, closePayload(1000)},
                                                Case{4001, 4001, closePayload(4001)},
                                                Case{std::nullopt, kCloseNoStatus, ""}}) {
    WsServer server;
    auto client = connected(server);
    server.sendClose(code);
    REQUIRE(closes(client));
    CHECK(client.closeCode() == closeCode);
    REQUIRE(server.waitFrames(1));
    const auto frame = server.frames().at(0);
    CHECK(frame.opcode == 0x8);
    CHECK(frame.masked);
    CHECK(frame.payload == echoed);
    CHECK(server.waitClientGone());
  }
}

TEST_CASE("ACloseCodeTheRfcDoesNotAllowIsAnsweredWith1002") {
  WsServer server;
  auto client = connected(server);
  server.sendClose(1006);
  REQUIRE(closes(client));
  CHECK(client.closeCode() == kCloseProtocolError);
  REQUIRE(server.waitFrames(1));
  CHECK(server.frames().at(0).payload == closePayload(1002));
}

TEST_CASE("AFloodOfDroppedFramesNeverHoldsTheOwner") {
  // 64 MiB of what next() drops (binary messages and pongs), then a text
  WsServer server;
  auto client = connected(server);
  const std::string chunk(std::size_t{64} << 10, 'b');
  for (int i = 0; i < 1024; ++i) {
    server.sendFrame(0x82, chunk);
    server.sendFrame(0x8A, "pong");
  }
  server.sendText("after");
  std::size_t yields = 0;
  std::size_t yieldsWithMore = 0;
  auto slowest = std::chrono::milliseconds{0};
  std::optional<std::string> text;
  const auto deadline = Clock::now() + 60s;
  while (!text && client.open() && Clock::now() < deadline) {
    const auto start = Clock::now();
    text = client.next();
    slowest = std::max(slowest, since(start));
    if (text || !client.open())
      break;
    ++yields;
    // the budget ended the drain with the socket still readable: the owner's poll returns at once
    pollfd ready{client.fd(), POLLIN, 0};
    if (::poll(&ready, 1, 0) == 1)
      ++yieldsWithMore;
    else
      ::poll(&ready, 1, 1000);
  }
  CHECK(text == "after");
  CHECK(slowest < 1s);
  // 64 MiB at most a budget and a chunk per drain is well over 200 drains; reading to the end would leave none
  CHECK(yields >= 200);
  CHECK(yieldsWithMore >= 100);
}

TEST_CASE("ATextFloodEndsADrainAtTheBudget") {
  // the owner takes texts until nullopt: about 8 MiB of them must not keep it from its poll
  WsServer server;
  auto client = connected(server);
  const std::string text(200, 't');
  constexpr int kTexts = 40000;
  for (int i = 0; i < kTexts; ++i)
    server.sendText(text);
  int received = 0;
  int drainsWithMore = 0;
  const auto deadline = Clock::now() + 60s;
  while (received < kTexts && client.open() && Clock::now() < deadline) {
    while (client.next())
      ++received;
    pollfd ready{client.fd(), POLLIN, 0};
    if (::poll(&ready, 1, 0) == 1)
      ++drainsWithMore;
    else
      ::poll(&ready, 1, 1000);
  }
  CHECK(received == kTexts);
  CHECK(drainsWithMore >= 5);
}

TEST_CASE("APingFloodCostsOnePongPerDrain") {
  WsServer server;
  auto client = connected(server);
  constexpr int kPings = 100000;
  for (int i = 0; i < kPings; ++i)
    server.sendFrame(0x89, std::to_string(i));
  server.sendText("after");
  std::size_t drains = 0;
  auto slowest = std::chrono::milliseconds{0};
  std::optional<std::string> text;
  const auto deadline = Clock::now() + 60s;
  while (!text && client.open() && Clock::now() < deadline) {
    const auto start = Clock::now();
    text = client.next();
    slowest = std::max(slowest, since(start));
    if (!text) {
      ++drains;
      pollfd ready{client.fd(), POLLIN, 0};
      ::poll(&ready, 1, 1000);
    }
  }
  REQUIRE(text == "after");
  // the drain that took the text ends here, with the pong of the last ping
  CHECK_FALSE(client.next().has_value());
  ++drains;
  CHECK(slowest < 1s);
  REQUIRE(server.waitFrames(1));
  REQUIRE(eventually([&] { return server.frames().back().payload == std::to_string(kPings - 1); }));
  const auto pongs = server.frames();
  CHECK(pongs.size() <= drains);
  CHECK(pongs.size() < 1000);
  for (const auto& pong : pongs) {
    CHECK(pong.opcode == 0xA);
    CHECK(pong.masked);
  }
  CHECK(client.open());
}

TEST_CASE("AHangupWithoutACloseIsAbnormal") {
  WsServer server;
  auto client = connected(server);
  server.hangUp();
  REQUIRE(closes(client));
  CHECK(client.closeCode() == kCloseAbnormal);
}

TEST_CASE("CloseSendsItsCodeThenClosesTheSocket") {
  WsServer server;
  auto client = connected(server);
  client.close(kCloseGoingAway);
  CHECK_FALSE(client.open());
  CHECK(client.fd() == -1);
  CHECK(client.closeCode() == kCloseGoingAway);
  REQUIRE(server.waitFrames(1));
  CHECK(server.frames().at(0).payload == closePayload(1001));
  CHECK(server.waitClientGone());
  // once is enough
  client.close(kCloseNormal);
  CHECK(client.closeCode() == kCloseGoingAway);
}

TEST_CASE("ASendThatStallsDropsTheConnection") {
  WsServer server({.reading = false});
  auto client = connected(server, {.sendTimeout = 300ms});
  // far more than the socket buffers on both ends hold while nothing reads
  const std::string huge(std::size_t{16} << 20, 'h');
  const auto start = Clock::now();
  CHECK_FALSE(client.send(huge));
  CHECK(since(start) >= 290ms);
  // the timeout runs from when the frame is built, which a sanitizer build takes its time over
  CHECK(since(start) < 5s);
  CHECK_FALSE(client.open());
  CHECK(client.closeCode() == kCloseAbnormal);
  CHECK_FALSE(client.send("small"));
}

TEST_CASE("ASendGivesUpWhenTheOwnerStops") {
  WsServer server({.reading = false});
  const int stop = ::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
  REQUIRE(stop >= 0);
  // far longer than the test takes: the stop, not the timeout, ends the wait
  auto client = connected(server, {.sendTimeout = 30s, .stopFd = stop});
  const std::uint64_t one = 1;
  REQUIRE(::write(stop, &one, sizeof one) == sizeof one);
  const auto start = Clock::now();
  CHECK_FALSE(client.send(std::string(std::size_t{16} << 20, 'h')));
  CHECK(since(start) < 5s);
  CHECK_FALSE(client.open());
  ::close(stop);
}

TEST_CASE("AMovedClientKeepsItsConnection") {
  WsServer server;
  auto first = connected(server);
  Client second = std::move(first);
  CHECK_FALSE(first.open());
  server.sendText("moved");
  CHECK(nextText(second) == "moved");
}
