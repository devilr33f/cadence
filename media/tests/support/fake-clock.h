#pragma once

#include "file/file-clock.h"

namespace cadence::test_support {

// time moves only when a test or a wait moves it
class FakeFileClock final : public FileClock {
 public:
  TimePoint now() override { return now_; }
  void waitUntil(TimePoint deadline) override {
    if (deadline > now_)
      now_ = deadline;
  }
  void advance(std::chrono::nanoseconds by) { now_ += std::chrono::duration_cast<TimePoint::duration>(by); }

 private:
  TimePoint now_{std::chrono::seconds(1000)};
};

}  // namespace cadence::test_support
