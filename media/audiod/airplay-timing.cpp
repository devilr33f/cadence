#include "audiod/airplay-timing.h"

#include <arpa/inet.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cerrno>

namespace cadence::audiod::airplay {

namespace {

// nqptp's own command for "make the interface", with its nul
constexpr char kNudge[] = "/nqptp T";

sockaddr_in addressOf(in_addr_t host, std::uint16_t port) {
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_addr.s_addr = htonl(host);
  address.sin_port = htons(port);
  return address;
}

TimingStamp stampOf(const struct stat& info) { return {.changed = info.st_ctim, .modified = info.st_mtim}; }

bool same(const timespec& a, const timespec& b) { return a.tv_sec == b.tv_sec && a.tv_nsec == b.tv_nsec; }

}  // namespace

Result<PortHold, SysError> PortHold::bind(const TimingPorts& ports) {
  const std::array<sockaddr_in, 3> addresses{addressOf(INADDR_ANY, ports.event), addressOf(INADDR_ANY, ports.general),
                                             addressOf(INADDR_LOOPBACK, ports.control)};
  std::array<UniqueFd, 3> sockets;
  for (std::size_t i = 0; i < addresses.size(); ++i) {
    sockets[i] = UniqueFd(::socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0));
    if (!sockets[i])
      return SysError{.call = "socket", .code = errno};
    if (::bind(sockets[i].get(), reinterpret_cast<const sockaddr*>(&addresses[i]), sizeof addresses[i]) != 0)
      return SysError{.call = "bind", .code = errno};
  }
  return PortHold(std::move(sockets));
}

bool TimingStamp::operator==(const TimingStamp& other) const {
  return same(changed, other.changed) && same(modified, other.modified);
}

std::optional<TimingStamp> timingStamp(const std::string& path) {
  struct stat info {};
  if (::lstat(path.c_str(), &info) != 0 || !S_ISREG(info.st_mode))
    return std::nullopt;
  return stampOf(info);
}

std::optional<SysError> removeStaleTiming(const std::string& path) {
  struct stat info {};
  if (::lstat(path.c_str(), &info) != 0)
    return errno == ENOENT ? std::nullopt : std::optional(SysError{.call = "lstat", .code = errno});
  if (!S_ISREG(info.st_mode))
    return SysError{.call = "lstat", .code = ELOOP};
  if (::unlink(path.c_str()) != 0 && errno != ENOENT)
    return SysError{.call = "unlink", .code = errno};
  return std::nullopt;
}

bool timingFresh(const std::string& path, const std::optional<TimingStamp>& previous) {
  const UniqueFd file(::open(path.c_str(), O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC));
  struct stat info {};
  if (!file || ::fstat(file.get(), &info) != 0 || !S_ISREG(info.st_mode))
    return false;
  // native byte order, as nqptp writes it for shairport on the same cpu
  std::uint16_t version = 0;
  if (::pread(file.get(), &version, sizeof version, 0) != static_cast<ssize_t>(sizeof version))
    return false;
  return version == kTimingVersion && (!previous || !(stampOf(info) == *previous));
}

std::optional<TimingNudge> TimingNudge::open() {
  UniqueFd socket(::socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC | SOCK_NONBLOCK, 0));
  if (!socket)
    return std::nullopt;
  return TimingNudge(std::move(socket));
}

// unconnected, so the port unreachable a missing nqptp answers with never fails the next one
void TimingNudge::send(std::uint16_t port) const {
  const auto address = addressOf(INADDR_LOOPBACK, port);
  [[maybe_unused]] const auto sent =
      ::sendto(socket_.get(), kNudge, sizeof kNudge, MSG_NOSIGNAL, reinterpret_cast<const sockaddr*>(&address),
               sizeof address);
}

}  // namespace cadence::audiod::airplay
