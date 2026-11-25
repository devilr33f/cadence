#include "audiod/soloist-resolv.h"

#include "resolv/resolv-wire.h"

#include <netdb.h>
#include <netinet/in.h>
#include <poll.h>
#include <sys/eventfd.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#include <array>
#include <cerrno>
#include <cstdint>
#include <cstring>
#include <optional>
#include <string>
#include <thread>
#include <utility>
#include <vector>

namespace cadence::audiod {

namespace {

using namespace cadence::resolv;

// a client that neither asks nor hangs up: the socket's reads give up, so one cannot hold the thread
constexpr int kClientTimeoutSeconds = 8;

std::vector<std::uint8_t> statusBody(Status status) {
  std::vector<std::uint8_t> body(kResponseHead, 0);
  put32(body.data(), static_cast<std::uint32_t>(status));
  return body;
}

// one frame's head, then as much of the body as arrives; nullopt on a short frame, a refusal or a timeout
std::optional<std::vector<std::uint8_t>> readFrame(int fd) {
  std::array<std::uint8_t, 4> head{};
  std::size_t got = 0;
  while (got < head.size()) {
    const auto size = ::recv(fd, head.data() + got, head.size() - got, 0);
    if (size == 0 || size < 0)
      return std::nullopt;
    got += static_cast<std::size_t>(size);
  }
  const auto len = get32(head.data());
  if (len == 0 || len > kMaxFrame)
    return std::nullopt;
  std::vector<std::uint8_t> body(len);
  got = 0;
  while (got < body.size()) {
    const auto size = ::recv(fd, body.data() + got, body.size() - got, 0);
    if (size == 0 || size < 0)
      return std::nullopt;
    got += static_cast<std::size_t>(size);
  }
  return body;
}

bool writeAll(int fd, const std::uint8_t* bytes, std::size_t len) {
  while (len > 0) {
    const auto sent = ::send(fd, bytes, len, MSG_NOSIGNAL);
    if (sent <= 0)
      return false;
    bytes += sent;
    len -= static_cast<std::size_t>(sent);
  }
  return true;
}

bool writeFrame(int fd, const std::vector<std::uint8_t>& body) {
  std::array<std::uint8_t, 4> head{};
  put32(head.data(), static_cast<std::uint32_t>(body.size()));
  return writeAll(fd, head.data(), head.size()) && writeAll(fd, body.data(), body.size());
}

std::optional<Request> parseRequest(const std::vector<std::uint8_t>& body) {
  if (body.size() < kRequestHead)
    return std::nullopt;
  Request out;
  out.family = static_cast<std::int32_t>(get32(body.data()));
  out.flags = get32(body.data() + 4);
  const auto nameLen = get32(body.data() + 8);
  const auto serviceLen = get32(body.data() + 12);
  if (nameLen == 0 || nameLen > kMaxName || serviceLen > kMaxService ||
      kRequestHead + nameLen + serviceLen != body.size())
    return std::nullopt;
  out.name = reinterpret_cast<const char*>(body.data()) + kRequestHead;
  out.nameLen = nameLen;
  out.service = reinterpret_cast<const char*>(body.data()) + kRequestHead + nameLen;
  out.serviceLen = serviceLen;
  if (serviceLen != 0 && !printable(out.service, serviceLen))
    return std::nullopt;
  return out;
}

// the wire's family numbers, as the resolver's own
int nativeFamily(std::int32_t family) {
  switch (family) {
    case 0:
      return AF_UNSPEC;
    case 2:
      return AF_INET;
    case 10:
      return AF_INET6;
    default:
      return -1;
  }
}

// the answer as the wire writes it: audiod's own lookup, through whatever resolver its libc holds (bionic's netd on
// android, the host's nss elsewhere), with the addresses and the canonical name it gave
std::vector<std::uint8_t> answer(const Request& request) {
  const int family = nativeFamily(request.family);
  if (family < 0)
    return statusBody(kFail);
  // a name the wire refuses is no name, as the resolver would answer it
  if (!printable(request.name, request.nameLen))
    return statusBody(kNoname);
  const std::string name(request.name, request.nameLen);
  const std::string service(request.service, request.serviceLen);
  struct addrinfo hints {};
  hints.ai_family = family;
  hints.ai_socktype = SOCK_STREAM;
  if ((request.flags & 0x1) != 0)
    hints.ai_flags |= AI_CANONNAME;
  if ((request.flags & 0x2) != 0)
    hints.ai_flags |= AI_NUMERICSERV;
  struct addrinfo* found = nullptr;
  const int code = ::getaddrinfo(name.c_str(), service.empty() ? nullptr : service.c_str(), &hints, &found);
  if (code != 0)
    return statusBody(code == EAI_NONAME ? kNoname : code == EAI_AGAIN ? kAgain : kFail);
  std::vector<std::uint8_t> body(kResponseHead, 0);
  std::string cname;
  std::uint32_t count = 0;
  for (auto* it = found; it != nullptr && count < kMaxAddrs; it = it->ai_next) {
    const bool inet6 = it->ai_family == AF_INET6;
    if (!inet6 && it->ai_family != AF_INET)
      continue;
    if (static_cast<std::size_t>(it->ai_addrlen) < (inet6 ? sizeof(sockaddr_in6) : sizeof(sockaddr_in)))
      continue;
    const auto* wide = reinterpret_cast<const sockaddr_in6*>(it->ai_addr);
    const auto* narrow = reinterpret_cast<const sockaddr_in*>(it->ai_addr);
    if (cname.empty() && (hints.ai_flags & AI_CANONNAME) != 0 && it->ai_canonname != nullptr) {
      cname.assign(it->ai_canonname);
      if (cname.size() > kMaxName)
        cname.resize(kMaxName);
    }
    // the wire's address: family, port and 16 address bytes (an inet one in the first 4), never a struct's padding
    std::array<std::uint8_t, kAddrBytes> wire{};
    put32(wire.data(), static_cast<std::uint32_t>(inet6 ? 10 : 2));
    put16(wire.data() + 4, ntohs(inet6 ? wide->sin6_port : narrow->sin_port));
    std::memcpy(wire.data() + 6, inet6 ? reinterpret_cast<const std::uint8_t*>(&wide->sin6_addr)
                                       : reinterpret_cast<const std::uint8_t*>(&narrow->sin_addr),
                inet6 ? 16 : 4);
    body.insert(body.end(), wire.begin(), wire.end());
    count++;
  }
  ::freeaddrinfo(found);
  if (count == 0)
    return statusBody(kNoname);
  put32(body.data() + 4, static_cast<std::uint32_t>(cname.size()));
  put32(body.data() + 8, count);
  body.insert(body.begin() + static_cast<std::ptrdiff_t>(kResponseHead), cname.begin(), cname.end());
  return body;
}

}  // namespace

struct SoloistResolv::State {
  State(Log l, UniqueFd stop, UniqueFd listener)
      : log(std::move(l)), stopper(std::move(stop)), socket(std::move(listener)) {}

  Log log;
  UniqueFd stopper;
  UniqueFd socket;
  std::thread thread;
};

std::unique_ptr<SoloistResolv> SoloistResolv::listen(const Log& log, const std::string& path) {
  if (path.size() >= sizeof(sockaddr_un::sun_path)) {
    log.warn("soloist_resolv_failed", {{"call", "path"}});
    return nullptr;
  }
  const int stop = ::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
  if (stop < 0) {
    log.warn("soloist_resolv_failed", {{"call", "eventfd"}, {"errno", errno}});
    return nullptr;
  }
  const int fd = ::socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
  if (fd < 0) {
    log.warn("soloist_resolv_failed", {{"call", "socket"}, {"errno", errno}});
    return nullptr;
  }
  ::unlink(path.c_str());
  sockaddr_un address{};
  address.sun_family = AF_UNIX;
  std::memcpy(address.sun_path, path.c_str(), path.size() + 1);
  if (::bind(fd, reinterpret_cast<const sockaddr*>(&address), sizeof address) != 0 || ::listen(fd, 4) != 0) {
    log.warn("soloist_resolv_failed", {{"call", "bind"}, {"errno", errno}});
    ::close(fd);
    return nullptr;
  }
  auto state = std::make_unique<State>(log, UniqueFd(stop), UniqueFd(fd));
  State* s = state.get();
  s->thread = std::thread([s] {
    for (;;) {
      std::array<pollfd, 2> ready{{ {.fd = s->stopper.get(), .events = POLLIN, .revents = 0},
                                    {.fd = s->socket.get(), .events = POLLIN, .revents = 0} }};
      const int polled = ::poll(ready.data(), ready.size(), -1);
      if (polled < 0 && errno != EINTR)
        return;
      if ((ready[0].revents & POLLIN) != 0)
        return;
      if ((ready[1].revents & POLLIN) == 0)
        continue;
      const int client = ::accept4(s->socket.get(), nullptr, nullptr, SOCK_CLOEXEC);
      if (client < 0)
        continue;
      const timeval timeout{.tv_sec = kClientTimeoutSeconds, .tv_usec = 0};
      ::setsockopt(client, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof timeout);
      ::setsockopt(client, SOL_SOCKET, SO_SNDTIMEO, &timeout, sizeof timeout);
      // one connection may ask more than once; a frame that is not ours ends it
      while (const auto body = readFrame(client)) {
        const auto request = parseRequest(*body);
        if (!writeFrame(client, request ? answer(*request) : statusBody(kFail)))
          break;
      }
      ::close(client);
    }
  });
  return std::unique_ptr<SoloistResolv>(new SoloistResolv(std::move(state)));
}

SoloistResolv::SoloistResolv(std::unique_ptr<State> state) : state_(std::move(state)) {}

SoloistResolv::~SoloistResolv() {
  const std::uint64_t one = 1;
  [[maybe_unused]] const auto written = ::write(state_->stopper.get(), &one, sizeof one);
  if (state_->thread.joinable())
    state_->thread.join();
}

}  // namespace cadence::audiod
