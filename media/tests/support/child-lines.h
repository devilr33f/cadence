#pragma once

#include "audiod/child-log.h"
#include "support/eventually.h"

#include <cstddef>
#include <memory>
#include <mutex>
#include <optional>
#include <string_view>
#include <utility>
#include <vector>

namespace cadence::test_support {

// the children's lines a target got, sent from any thread and read from the test's
class ChildLines {
 public:
  audiod::ChildLogTarget target() {
    return {.forward = forward_, .sink = [this](audiod::ChildLine line) {
              std::scoped_lock lock(mutex_);
              got_.push_back(std::move(line));
            }};
  }

  [[nodiscard]] std::vector<audiod::ChildLine> got() const {
    std::scoped_lock lock(mutex_);
    return got_;
  }

  [[nodiscard]] bool has(std::size_t n) const {
    return eventually([&] { return got().size() >= n; });
  }

  // the first line whose text holds fragment, waited for
  [[nodiscard]] std::optional<audiod::ChildLine> find(std::string_view fragment) const {
    std::optional<audiod::ChildLine> found;
    eventually([&] {
      for (const auto& line : got()) {
        if (line.text.find(fragment) != std::string::npos) {
          found = line;
          return true;
        }
      }
      return false;
    });
    return found;
  }

 private:
  std::shared_ptr<audiod::ChildLogSwitch> forward_ = std::make_shared<audiod::ChildLogSwitch>();
  mutable std::mutex mutex_;
  std::vector<audiod::ChildLine> got_;
};

}  // namespace cadence::test_support
