#pragma once

#include "audiod/log.h"
#include "support/eventually.h"

#include <chrono>
#include <cstddef>
#include <mutex>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

namespace cadence::test_support {

// log lines with when they came, readable from the test thread
class TimedLog {
 public:
  using Clock = std::chrono::steady_clock;

  audiod::Log log() {
    return audiod::Log([this](std::string_view line) {
      std::scoped_lock lock(mutex_);
      lines_.emplace_back(Clock::now(), std::string(line));
    });
  }

  [[nodiscard]] std::size_t count(std::string_view fragment) const { return times(fragment).size(); }
  [[nodiscard]] std::vector<Clock::time_point> times(std::string_view fragment) const {
    std::scoped_lock lock(mutex_);
    std::vector<Clock::time_point> found;
    for (const auto& [at, line] : lines_)
      if (line.find(fragment) != std::string::npos)
        found.push_back(at);
    return found;
  }
  [[nodiscard]] std::string all() const {
    std::scoped_lock lock(mutex_);
    std::string text;
    for (const auto& entry : lines_)
      text += entry.second;
    return text;
  }
  bool eventually(std::string_view fragment, std::size_t atLeast = 1,
                  std::chrono::milliseconds limit = std::chrono::seconds(10)) const {
    return test_support::eventually([&] { return count(fragment) >= atLeast; }, limit);
  }

 private:
  mutable std::mutex mutex_;
  std::vector<std::pair<Clock::time_point, std::string>> lines_;
};

inline std::string msg(std::string_view name) { return "\"msg\":\"" + std::string(name) + "\""; }

}  // namespace cadence::test_support
