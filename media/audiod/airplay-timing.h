#pragma once

#include "audiod/sys.h"
#include "result.h"

#include <array>
#include <cstdint>
#include <ctime>
#include <optional>
#include <string>
#include <utility>

// nqptp, the timing service shairport's airplay 2 needs (reference §2.2 step 5): its ports, its shared memory interface
// and the datagram that makes it create one. a foreign daemon on its ports is never killed or adopted
namespace cadence::audiod::airplay {

// the interface's version, smiN in shairport's -V: the runtime build checks the two match (smi11)
inline constexpr std::uint16_t kTimingVersion = 11;

struct TimingPorts {
  std::uint16_t event = 319;
  std::uint16_t general = 320;
  // on 127.0.0.1
  std::uint16_t control = 9000;
};

// the three udp ports nqptp binds, bound here first to show they are free, and held while the stale interface goes;
// released before nqptp starts, as it binds them itself. close-on-exec, so no other child keeps one
class PortHold {
 public:
  // the errno of the first bind that failed, EADDRINUSE for a port another daemon holds
  static Result<PortHold, SysError> bind(const TimingPorts& ports);

 private:
  explicit PortHold(std::array<UniqueFd, 3> sockets) : sockets_(std::move(sockets)) {}

  std::array<UniqueFd, 3> sockets_;
};

// when the interface last changed: a killed nqptp leaves its interface with the stamp it had
struct TimingStamp {
  timespec changed{};
  timespec modified{};

  bool operator==(const TimingStamp& other) const;
};

// the stamp of a regular file at path, never through a symlink
std::optional<TimingStamp> timingStamp(const std::string& path);
// a killed nqptp's interface, a regular file, removed; a symlink or a directory is refused (ELOOP) and never followed
std::optional<SysError> removeStaleTiming(const std::string& path);
// nqptp's interface is at path, of kTimingVersion, and not the one stamped previous
bool timingFresh(const std::string& path, const std::optional<TimingStamp>& previous);

// "/nqptp T" and its nul to 127.0.0.1:port: nqptp makes its interface only once a control datagram came
class TimingNudge {
 public:
  static std::optional<TimingNudge> open();
  void send(std::uint16_t port) const;

 private:
  explicit TimingNudge(UniqueFd socket) : socket_(std::move(socket)) {}

  UniqueFd socket_;
};

}  // namespace cadence::audiod::airplay
