#pragma once

#include <cstdint>

namespace cadence::screen::ipc {

// reconnect delays: doubling from min to max, back to min after a link that held for stableMs
class Backoff {
 public:
  Backoff(std::int64_t minMs = 100, std::int64_t maxMs = 5000, std::int64_t stableMs = 10000)
      : minMs_(minMs), maxMs_(maxMs), stableMs_(stableMs), nextMs_(minMs) {}

  // the delay before the next attempt
  std::int64_t next();
  // a connection held for heldMs before it went
  void connectedFor(std::int64_t heldMs);
  void reset() { nextMs_ = minMs_; }

 private:
  std::int64_t minMs_;
  std::int64_t maxMs_;
  std::int64_t stableMs_;
  std::int64_t nextMs_;
};

}  // namespace cadence::screen::ipc
