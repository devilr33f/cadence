#include "ipc/backoff.h"

#include <algorithm>

namespace cadence::screen::ipc {

std::int64_t Backoff::next() {
  const std::int64_t out = nextMs_;
  nextMs_ = std::min(maxMs_, nextMs_ * 2);
  return out;
}

void Backoff::connectedFor(std::int64_t heldMs) {
  if (heldMs >= stableMs_) reset();
}

}  // namespace cadence::screen::ipc
