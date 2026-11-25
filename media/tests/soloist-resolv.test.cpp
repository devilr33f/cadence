// soloist's resolv socket and the preload that asks it (media/audiod/soloist-resolv.cpp, media/pulse-shim/
// resolv-preload.cpp): the server over the wire the two share, and the preload through it end to end, against the
// host's own resolver, as audiod's is the resolver on the device
#include "audiod/soloist-resolv.h"
#include "resolv/resolv-wire.h"
#include "support/audiod-harness.h"

#include <arpa/inet.h>
#include <netdb.h>
#include <signal.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <unistd.h>

#include <catch2/catch_test_macros.hpp>

#include <array>
#include <cerrno>
#include <cstring>
#include <memory>
#include <string>
#include <vector>

using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace cadence::resolv;

namespace {

// the raw client the wire promises: one frame in, one frame out
class WireClient {
 public:
  explicit WireClient(const std::string& path) {
    fd_ = ::socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
    sockaddr_un address{};
    address.sun_family = AF_UNIX;
    std::memcpy(address.sun_path, path.c_str(), path.size() + 1);
    REQUIRE(::connect(fd_, reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
  }
  ~WireClient() { ::close(fd_); }

  WireClient(const WireClient&) = delete;
  WireClient& operator=(const WireClient&) = delete;

  void send(const std::uint8_t* body, std::uint32_t len) {
    std::array<std::uint8_t, 4> head{};
    put32(head.data(), len);
    REQUIRE(::send(fd_, head.data(), head.size(), 0) == 4);
    std::size_t sent = 0;
    while (sent < len) {
      const auto n = ::send(fd_, body + sent, len - sent, 0);
      REQUIRE(n > 0);
      sent += static_cast<std::size_t>(n);
    }
  }

  std::vector<std::uint8_t> receive() {
    std::array<std::uint8_t, 4> head{};
    std::size_t got = 0;
    while (got < 4) {
      const auto n = ::recv(fd_, head.data() + got, 4 - got, 0);
      REQUIRE(n > 0);
      got += static_cast<std::size_t>(n);
    }
    const auto len = get32(head.data());
    REQUIRE(len > 0);
    std::vector<std::uint8_t> body(len);
    got = 0;
    while (got < len) {
      const auto n = ::recv(fd_, body.data() + got, len - got, 0);
      REQUIRE(n > 0);
      got += static_cast<std::size_t>(n);
    }
    return body;
  }

 private:
  int fd_ = -1;
};

std::vector<std::uint8_t> request(std::int32_t family, std::uint32_t flags, const std::string& name,
                                  const std::string& service = {}) {
  std::vector<std::uint8_t> body(kRequestHead + name.size() + service.size(), 0);
  put32(body.data(), static_cast<std::uint32_t>(family));
  put32(body.data() + 4, flags);
  put32(body.data() + 8, static_cast<std::uint32_t>(name.size()));
  put32(body.data() + 12, static_cast<std::uint32_t>(service.size()));
  std::memcpy(body.data() + kRequestHead, name.data(), name.size());
  if (!service.empty())
    std::memcpy(body.data() + kRequestHead + name.size(), service.data(), service.size());
  return body;
}

// the server in a child forked before the preload is pointed at it: compiled into this binary, the preload is every
// getaddrinfo here, the server's too, which would ask its own socket (audiod is never preloaded); the child's copy of
// the environment never names the socket, so its lookups fall back to libc's
class ServerProcess {
 public:
  explicit ServerProcess(const std::string& path) {
    int ready[2];
    REQUIRE(::pipe(ready) == 0);
    const pid_t parent = ::getpid();
    pid_ = ::fork();
    REQUIRE(pid_ >= 0);
    if (pid_ == 0) {
      ::close(ready[0]);
      ::prctl(PR_SET_PDEATHSIG, SIGKILL);
      if (::getppid() != parent)
        ::_exit(1);
      LogCapture logs;
      const auto server = SoloistResolv::listen(logs.log(), path);
      const char ok = server ? 1 : 0;
      if (::write(ready[1], &ok, 1) != 1 || !server)
        ::_exit(1);
      for (;;)
        ::pause();
    }
    ::close(ready[1]);
    char ok = 0;
    const auto got = ::read(ready[0], &ok, 1);
    ::close(ready[0]);
    if (got != 1 || ok != 1)
      stop();
    REQUIRE(got == 1);
    REQUIRE(ok == 1);
  }
  ~ServerProcess() { stop(); }

  ServerProcess(const ServerProcess&) = delete;
  ServerProcess& operator=(const ServerProcess&) = delete;

 private:
  void stop() {
    if (pid_ <= 0)
      return;
    ::kill(pid_, SIGKILL);
    while (::waitpid(pid_, nullptr, 0) < 0 && errno == EINTR) {
    }
    pid_ = -1;
  }

  pid_t pid_ = -1;
};

}  // namespace

TEST_CASE("the resolv socket answers a lookup with the host's own resolver") {
  TempDir dir;
  LogCapture logs;
  const std::string path = dir.file("resolv.sock");
  auto server = SoloistResolv::listen(logs.log(), path);
  REQUIRE(server);

  SECTION("localhost resolves, with its addresses and the asked-for canonname") {
    WireClient client(path);
    const auto ask = request(0, 0x1, "localhost");
    client.send(ask.data(), static_cast<std::uint32_t>(ask.size()));
    const auto body = client.receive();
    REQUIRE(body.size() >= kResponseHead);
    CHECK(static_cast<std::int32_t>(get32(body.data())) == kOk);
    const auto cnameLen = get32(body.data() + 4);
    const auto count = get32(body.data() + 8);
    CHECK(cnameLen > 0);
    CHECK(count > 0);
    REQUIRE(kResponseHead + cnameLen + count * kAddrBytes == body.size());
    const std::string cname(reinterpret_cast<const char*>(body.data()) + kResponseHead, cnameLen);
    CHECK(cname.find("localhost") != std::string::npos);
    bool loopback = false;
    for (std::uint32_t i = 0; i < count; i++) {
      const std::uint8_t* address = body.data() + kResponseHead + cnameLen + i * kAddrBytes;
      const auto family = static_cast<std::int32_t>(get32(address));
      char text[INET6_ADDRSTRLEN] = {};
      if (family == 2) {
        ::inet_ntop(AF_INET, address + 6, text, sizeof text);
        loopback = loopback || std::string(text) == "127.0.0.1";
      } else if (family == 10) {
        ::inet_ntop(AF_INET6, address + 6, text, sizeof text);
        loopback = loopback || std::string(text) == "::1";
      } else {
        FAIL("a family the wire does not carry");
      }
    }
    CHECK(loopback);
  }

  SECTION("a name the wire refuses, and a service, come back as the wire's answers") {
    WireClient client(path);
    const auto refused = request(0, 0, std::string("bad\x01name"));
    client.send(refused.data(), static_cast<std::uint32_t>(refused.size()));
    const auto body = client.receive();
    REQUIRE(body.size() == kResponseHead);
    CHECK(static_cast<std::int32_t>(get32(body.data())) == kNoname);

    const auto port = request(2, 0x2, "localhost", "80");
    client.send(port.data(), static_cast<std::uint32_t>(port.size()));
    const auto answered = client.receive();
    REQUIRE(static_cast<std::int32_t>(get32(answered.data())) == kOk);
    const auto count = get32(answered.data() + 8);
    REQUIRE(count > 0);
    const auto cnameLen2 = get32(answered.data() + 4);
    const std::uint8_t* first = answered.data() + kResponseHead + cnameLen2;
    CHECK(static_cast<std::int32_t>(get32(first)) == 2);
    CHECK(get16(first + 4) == 80);
  }
}

TEST_CASE("the preload answers getaddrinfo through the socket, and frees what it made") {
  TempDir dir;
  const std::string path = dir.file("resolv.sock");
  ServerProcess server(path);
  ::setenv("CADENCE_RESOLV_SOCK", path.c_str(), 1);

  struct addrinfo hints {};
  hints.ai_family = AF_INET;
  hints.ai_socktype = SOCK_STREAM;
  hints.ai_flags = AI_CANONNAME;
  struct addrinfo* found = nullptr;
  const int code = ::getaddrinfo("localhost", "80", &hints, &found);
  if (code == 0) {
    REQUIRE(found != nullptr);
    bool loopback = false;
    for (auto* it = found; it != nullptr; it = it->ai_next) {
      CHECK(it->ai_family == AF_INET);
      CHECK(it->ai_socktype == SOCK_STREAM);
      const auto* address = reinterpret_cast<const sockaddr_in*>(it->ai_addr);
      loopback = loopback || ntohl(address->sin_addr.s_addr) == 0x7f000001;
      CHECK(ntohs(address->sin_port) == 80);
    }
    CHECK(loopback);
    CHECK(found->ai_canonname != nullptr);
    ::freeaddrinfo(found);
  } else {
    // a host whose /etc/hosts names no localhost: the socket still answered through its own resolver, or fell back
    CHECK(code == EAI_NONAME);
  }

  ::setenv("CADENCE_RESOLV_SOCK", "/nonexistent/resolv.sock", 1);
  hints.ai_flags = 0;
  found = nullptr;
  // no socket: the preload hands the call to the real getaddrinfo
  CHECK(::getaddrinfo("localhost", nullptr, &hints, &found) == 0);
  ::freeaddrinfo(found);
  ::unsetenv("CADENCE_RESOLV_SOCK");
}
