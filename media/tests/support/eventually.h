#pragma once

#include <chrono>
#include <thread>

namespace cadence::test_support {

// polls while background threads run; false if the condition never held
template <typename Condition>
bool eventually(Condition condition, std::chrono::milliseconds limit = std::chrono::seconds(5)) {
  const auto deadline = std::chrono::steady_clock::now() + limit;
  while (!condition()) {
    if (std::chrono::steady_clock::now() > deadline)
      return false;
    std::this_thread::sleep_for(std::chrono::milliseconds(1));
  }
  return true;
}

}  // namespace cadence::test_support
