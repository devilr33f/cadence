#pragma once

#include <chrono>

namespace cadence {

// the time source of the file backends, so tests can step it by hand
class FileClock {
 public:
  using TimePoint = std::chrono::steady_clock::time_point;

  virtual ~FileClock() = default;
  [[nodiscard]] virtual TimePoint now() = 0;
  // returns once now() has reached deadline; a deadline in the past returns at once
  virtual void waitUntil(TimePoint deadline) = 0;
};

// steady_clock and sleep_until
FileClock& realFileClock();

}  // namespace cadence
