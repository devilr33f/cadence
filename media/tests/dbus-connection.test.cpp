#include "dbus/bus.h"
#include "dbus/client.h"
#include "dbus/connection.h"
#include "dbus/message.h"
#include "support/temp-dir.h"

#include <catch2/catch_test_macros.hpp>

#include <poll.h>
#include <sys/eventfd.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#include <chrono>
#include <cstring>
#include <functional>
#include <future>
#include <optional>
#include <string>
#include <thread>
#include <vector>

using namespace cadence::dbus;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

using Bytes = std::vector<std::uint8_t>;

const std::string kOk = "OK 0123456789abcdef0123456789abcdef\r\n";

// bytes from a blocking socket until text ends with terminator; what came, empty on a hangup or after 5 s
std::string readUntil(int fd, std::string_view terminator) {
  std::string got;
  const auto deadline = std::chrono::steady_clock::now() + 5s;
  while (got.size() < terminator.size() || got.compare(got.size() - terminator.size(), terminator.size(),
                                                       terminator) != 0) {
    pollfd ready{fd, POLLIN, 0};
    if (std::chrono::steady_clock::now() > deadline || ::poll(&ready, 1, 100) < 0)
      return {};
    if (ready.revents == 0)
      continue;
    char byte = 0;
    if (::recv(fd, &byte, 1, 0) != 1)
      return {};
    got += byte;
  }
  return got;
}

void sendAll(int fd, std::string_view bytes) {
  while (!bytes.empty()) {
    const auto sent = ::send(fd, bytes.data(), bytes.size(), MSG_NOSIGNAL);
    if (sent <= 0)
      return;
    bytes.remove_prefix(static_cast<std::size_t>(sent));
  }
}

void sendAll(int fd, const Bytes& bytes) {
  sendAll(fd, std::string_view(reinterpret_cast<const char*>(bytes.data()), bytes.size()));
}

// the whole message the client sends next; one reader per connection keeps what a read took past it
std::optional<Message> readMessage(int fd, MessageReader& reader) {
  const auto deadline = std::chrono::steady_clock::now() + 5s;
  while (std::chrono::steady_clock::now() < deadline) {
    if (auto message = reader.next())
      return message;
    if (reader.failure())
      return std::nullopt;
    pollfd ready{fd, POLLIN, 0};
    if (::poll(&ready, 1, 100) <= 0)
      continue;
    std::uint8_t chunk[4096];
    const auto got = ::recv(fd, chunk, sizeof chunk, 0);
    if (got <= 0)
      return std::nullopt;
    reader.append(std::span(chunk, static_cast<std::size_t>(got)));
  }
  return std::nullopt;
}

// the one message a script reads
std::optional<Message> readMessage(int fd) {
  MessageReader reader;
  return readMessage(fd, reader);
}

// the authentication a well-behaved bus does; what the client sent before BEGIN
std::string authenticate(int fd) {
  const auto greeting = readUntil(fd, "\r\n");
  sendAll(fd, kOk);
  return greeting + readUntil(fd, "\r\n");
}

// for the fake's thread, where no catch2 assertion may run: empty when refused, which the client then never sees
Bytes encoded(Message message, std::uint32_t serial) {
  message.serial = serial;
  auto bytes = encodeMessage(message);
  return bytes.ok() ? std::move(bytes.value()) : Bytes{};
}

// a server on a unix socket in a fresh directory that serves one connection with script, on its own thread
class FakeBus {
 public:
  explicit FakeBus(std::function<void(int fd)> script) {
    listener_ = ::socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
    REQUIRE(listener_ >= 0);
    sockaddr_un address{};
    address.sun_family = AF_UNIX;
    const auto path = dir_.file("bus.sock");
    REQUIRE(path.size() < sizeof address.sun_path);
    std::memcpy(address.sun_path, path.c_str(), path.size() + 1);
    REQUIRE(::bind(listener_, reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
    REQUIRE(::listen(listener_, 1) == 0);
    address_ = "unix:path=" + path;
    thread_ = std::thread([this, script = std::move(script)] {
      const int fd = ::accept4(listener_, nullptr, nullptr, SOCK_CLOEXEC);
      if (fd < 0)
        return;
      script(fd);
      ::close(fd);
    });
  }
  ~FakeBus() {
    ::shutdown(listener_, SHUT_RDWR);
    thread_.join();
    ::close(listener_);
  }
  FakeBus(const FakeBus&) = delete;
  FakeBus& operator=(const FakeBus&) = delete;

  [[nodiscard]] const std::string& address() const { return address_; }

 private:
  TempDir dir_;
  int listener_ = -1;
  std::string address_;
  std::thread thread_;
};

Connection connected(const FakeBus& bus, ConnectionOptions options = {}) {
  auto result = Connection::connect(bus.address(), options);
  REQUIRE(result.ok());
  return std::move(result.value());
}

// drives the connection until its next message; nullopt once it closed or after 5 s
std::optional<Message> nextMessage(Connection& connection) {
  const auto deadline = std::chrono::steady_clock::now() + 5s;
  while (std::chrono::steady_clock::now() < deadline) {
    if (auto message = connection.next())
      return message;
    if (!connection.open())
      return std::nullopt;
    pollfd ready{connection.fd(), POLLIN, 0};
    ::poll(&ready, 1, 100);
  }
  return std::nullopt;
}

std::string hex(std::string_view text) {
  static constexpr char kDigits[] = "0123456789abcdef";
  std::string out;
  for (const char c : text) {
    out += kDigits[static_cast<unsigned char>(c) >> 4];
    out += kDigits[static_cast<unsigned char>(c) & 0xf];
  }
  return out;
}

}  // namespace

TEST_CASE("authenticates with EXTERNAL as its uid, begins without fd passing, then exchanges messages") {
  std::string sentBeforeMessages;
  std::optional<Message> received;
  {
    FakeBus bus([&](int fd) {
      sentBeforeMessages = authenticate(fd);
      received = readMessage(fd);
      if (!received)
        return;
      sendAll(fd, encoded(Message::methodReturn(*received, {Value::string(":1.1")}), 1));
      readUntil(fd, "never");
    });
    auto connection = connected(bus);
    const auto serial = connection.send(helloCall());
    CHECK(serial == 1);
    const auto reply = nextMessage(connection);
    REQUIRE(reply);
    CHECK(reply->type == MessageType::MethodReturn);
    CHECK(reply->replySerial == serial);
    CHECK(reply->body == std::vector<Value>{Value::string(":1.1")});
    connection.close();
    CHECK(connection.closeCode() == CloseCode::Closed);
    CHECK(connection.fd() == -1);
  }
  CHECK(sentBeforeMessages == std::string("\0AUTH EXTERNAL ", 15) + hex(std::to_string(::getuid())) + "\r\nBEGIN\r\n");
  REQUIRE(received);
  CHECK(received->member == "Hello");
  CHECK(received->serial == 1);
}

TEST_CASE("serials count up from 1, and a message that breaks a rule is not sent but leaves it open") {
  std::vector<std::uint32_t> serials;
  {
    FakeBus bus([&](int fd) {
      authenticate(fd);
      MessageReader reader;
      while (auto message = readMessage(fd, reader))
        serials.push_back(message->serial);
    });
    auto connection = connected(bus);
    CHECK(connection.send(listNamesCall()) == 1);
    auto broken = listNamesCall();
    broken.member = "not.a.member";
    CHECK(connection.send(broken) == 0);
    CHECK(connection.open());
    CHECK(connection.send(listNamesCall()) == 2);
  }
  CHECK(serials == std::vector<std::uint32_t>{1, 2});
}

TEST_CASE("connecting fails with a code") {
  SECTION("a rejected EXTERNAL") {
    FakeBus bus([](int fd) {
      readUntil(fd, "\r\n");
      sendAll(fd, "REJECTED DBUS_COOKIE_SHA1\r\n");
      readUntil(fd, "never");
    });
    const auto result = Connection::connect(bus.address());
    REQUIRE_FALSE(result.ok());
    CHECK(result.error() == ConnectError::Auth);
  }
  SECTION("a server that never answers, within connectTimeout") {
    FakeBus bus([](int fd) { readUntil(fd, "never"); });
    const auto start = std::chrono::steady_clock::now();
    const auto result = Connection::connect(bus.address(), {.connectTimeout = 200ms});
    REQUIRE_FALSE(result.ok());
    CHECK(result.error() == ConnectError::Timeout);
    CHECK(std::chrono::steady_clock::now() - start < 2s);
  }
  SECTION("the stop fd, at once") {
    FakeBus bus([](int fd) { readUntil(fd, "never"); });
    const int stop = ::eventfd(1, EFD_CLOEXEC);
    const auto start = std::chrono::steady_clock::now();
    const auto result = Connection::connect(bus.address(), {.connectTimeout = 5s, .stopFd = stop});
    ::close(stop);
    REQUIRE_FALSE(result.ok());
    CHECK(result.error() == ConnectError::Stopped);
    CHECK(std::chrono::steady_clock::now() - start < 1s);
  }
  SECTION("nothing listening") {
    TempDir dir;
    CHECK(Connection::connect("unix:path=" + dir.file("none.sock")).error() == ConnectError::Connect);
  }
  SECTION("an address that is not unix:path=") {
    CHECK(Connection::connect("unix:abstract=/x").error() == ConnectError::Address);
    CHECK(Connection::connect("unix:path=/" + std::string(200, 'a')).error() == ConnectError::Address);
  }
}

TEST_CASE("a peer that breaks the protocol closes the connection with a code") {
  SECTION("a malformed message") {
    FakeBus bus([](int fd) {
      authenticate(fd);
      auto bytes = encoded(Message::signal("/a", "org.a.B", "C"), 1);
      if (bytes.empty())
        return;
      bytes[0] = 'x';
      sendAll(fd, bytes);
      readUntil(fd, "never");
    });
    auto connection = connected(bus);
    CHECK_FALSE(nextMessage(connection));
    CHECK(connection.closeCode() == CloseCode::Malformed);
    CHECK(connection.wireError() == WireError::Endianness);
  }
  SECTION("an oversized one, from its first 16 bytes") {
    FakeBus bus([](int fd) {
      authenticate(fd);
      auto bytes = encoded(Message::signal("/a", "org.a.B", "C"), 1);
      if (bytes.empty())
        return;
      bytes[6] = 0x20;
      sendAll(fd, Bytes(bytes.begin(), bytes.begin() + 16));
      readUntil(fd, "never");
    });
    auto connection = connected(bus);
    CHECK_FALSE(nextMessage(connection));
    CHECK(connection.closeCode() == CloseCode::Malformed);
    CHECK(connection.wireError() == WireError::TooBig);
  }
  SECTION("a hangup") {
    FakeBus bus([](int fd) { authenticate(fd); });
    auto connection = connected(bus);
    CHECK_FALSE(nextMessage(connection));
    CHECK(connection.closeCode() == CloseCode::Hangup);
    CHECK_FALSE(connection.wireError());
    CHECK(connection.send(listNamesCall()) == 0);
  }
}

TEST_CASE("one drain reads at most the read budget and leaves the rest readable for the owner's next poll") {
  CHECK(ConnectionOptions{}.readBudget == kReadBudget);
  // about 21 KiB of signals, all in the socket before the first drain, against a budget of 4 KiB
  constexpr std::size_t kSignals = 20;
  std::promise<void> written;
  auto allWritten = written.get_future();
  FakeBus bus([&](int fd) {
    authenticate(fd);
    const std::string padding(1000, 'x');
    Bytes all;
    for (std::uint32_t serial = 1; serial <= kSignals; ++serial) {
      const auto bytes = encoded(Message::signal("/a", "org.a.B", "C", {Value::string(padding)}), serial);
      all.insert(all.end(), bytes.begin(), bytes.end());
    }
    sendAll(fd, all);
    written.set_value();
    readUntil(fd, "never");
  });
  auto connection = connected(bus, {.readBudget = 4096});
  REQUIRE(allWritten.wait_for(5s) == std::future_status::ready);

  std::size_t received = 0;
  while (connection.next())
    ++received;
  CHECK(received > 0);
  CHECK(received * 1000 <= 4096);
  pollfd ready{connection.fd(), POLLIN, 0};
  CHECK(::poll(&ready, 1, 0) == 1);

  const auto deadline = std::chrono::steady_clock::now() + 5s;
  while (received < kSignals && connection.open() && std::chrono::steady_clock::now() < deadline) {
    while (connection.next())
      ++received;
    ::poll(&ready, 1, 100);
  }
  CHECK(received == kSignals);
}

TEST_CASE("a peer that stops reading drops the connection within sendTimeout") {
  FakeBus bus([](int fd) {
    authenticate(fd);
    // read nothing more: only the client's hangup ends the wait
    pollfd hangup{fd, POLLRDHUP, 0};
    ::poll(&hangup, 1, 10000);
  });
  auto connection = connected(bus, {.sendTimeout = 200ms});
  const auto start = std::chrono::steady_clock::now();
  auto big = Message::signal("/a", "org.a.B", "C", {Value::bytes(std::vector<std::uint8_t>(900000, 1))});
  std::uint32_t sent = 1;
  for (int i = 0; i < 20 && sent != 0; ++i)
    sent = connection.send(big);
  CHECK(sent == 0);
  CHECK(connection.closeCode() == CloseCode::SendTimeout);
  CHECK(std::chrono::steady_clock::now() - start < 3s);
}

TEST_CASE("a client's Hello gives its unique name, dropping what the bus sends before the answer") {
  FakeBus bus([](int fd) {
    authenticate(fd);
    const auto hello = readMessage(fd);
    if (!hello)
      return;
    auto early = Message::signal("/org/freedesktop/DBus", "org.freedesktop.DBus", "NameAcquired",
                                 {Value::string(":1.9")});
    early.sender = "org.freedesktop.DBus";
    sendAll(fd, encoded(early, 1));
    auto answer = Message::methodReturn(*hello, {Value::string(":1.9")});
    answer.sender = "org.freedesktop.DBus";
    sendAll(fd, encoded(answer, 2));
    readUntil(fd, "never");
  });
  auto client = Client::connect(bus.address());
  REQUIRE(client.ok());
  CHECK(client.value().uniqueName() == ":1.9");
  CHECK(client.value().open());
}

TEST_CASE("what came with Hello's answer waits in the buffer, and hasBuffered says to process it before polling") {
  FakeBus bus([](int fd) {
    authenticate(fd);
    const auto hello = readMessage(fd);
    if (!hello)
      return;
    auto answer = Message::methodReturn(*hello, {Value::string(":1.9")});
    answer.sender = "org.freedesktop.DBus";
    auto acquired = Message::signal("/org/freedesktop/DBus", "org.freedesktop.DBus", "NameAcquired",
                                    {Value::string(":1.9")});
    acquired.sender = "org.freedesktop.DBus";
    acquired.destination = ":1.9";
    // one write, so the client reads both at once
    auto bytes = encoded(answer, 1);
    const auto more = encoded(acquired, 2);
    bytes.insert(bytes.end(), more.begin(), more.end());
    sendAll(fd, bytes);
    readUntil(fd, "never");
  });
  auto connected = Client::connect(bus.address());
  REQUIRE(connected.ok());
  auto& client = connected.value();
  CHECK(client.hasBuffered());
  pollfd ready{client.fd(), POLLIN, 0};
  CHECK(::poll(&ready, 1, 0) == 0);

  std::vector<std::string> members;
  client.setSignalHandler([&members](const Message& signal) { members.push_back(signal.member); });
  client.process();
  CHECK(members == std::vector<std::string>{"NameAcquired"});
  CHECK_FALSE(client.hasBuffered());
}

TEST_CASE("a client's Hello that fails is a connect error") {
  SECTION("answered with an error") {
    FakeBus bus([](int fd) {
      authenticate(fd);
      if (const auto hello = readMessage(fd))
        sendAll(fd, encoded(Message::error(*hello, "org.freedesktop.DBus.Error.Failed", "no"), 1));
      readUntil(fd, "never");
    });
    CHECK(Client::connect(bus.address()).error() == ConnectError::Hello);
  }
  SECTION("answered with something that is not a unique name") {
    FakeBus bus([](int fd) {
      authenticate(fd);
      if (const auto hello = readMessage(fd))
        sendAll(fd, encoded(Message::methodReturn(*hello, {Value::string("org.a.B")}), 1));
      readUntil(fd, "never");
    });
    CHECK(Client::connect(bus.address()).error() == ConnectError::Hello);
  }
  SECTION("a hangup") {
    FakeBus bus([](int fd) {
      authenticate(fd);
      (void)readMessage(fd);
    });
    CHECK(Client::connect(bus.address()).error() == ConnectError::Hello);
  }
  SECTION("no answer within connectTimeout") {
    FakeBus bus([](int fd) {
      authenticate(fd);
      readUntil(fd, "never");
    });
    const auto start = std::chrono::steady_clock::now();
    CHECK(Client::connect(bus.address(), {.connectTimeout = 300ms}).error() == ConnectError::Timeout);
    CHECK(std::chrono::steady_clock::now() - start < 2s);
  }
}
