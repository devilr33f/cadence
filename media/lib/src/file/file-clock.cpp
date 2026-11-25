#include "file/file-clock.h"

#include <thread>

namespace cadence {

namespace {

class RealFileClock final : public FileClock {
 public:
  TimePoint now() override { return std::chrono::steady_clock::now(); }
  void waitUntil(TimePoint deadline) override { std::this_thread::sleep_until(deadline); }
};

}  // namespace

FileClock& realFileClock() {
  static RealFileClock clock;
  return clock;
}

}  // namespace cadence
