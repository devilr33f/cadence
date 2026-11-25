#pragma once

#include "audiod/child-log.h"
#include "audiod/sys.h"

#include <cstddef>
#include <memory>
#include <mutex>
#include <string_view>
#include <thread>
#include <vector>

namespace cadence::audiod {

// drains children's stdout and stderr on a thread of its own, each child's into its ChildLog (M6b-D7), so a chatty
// child never blocks on a full pipe and never holds up the thread that runs it. a round reads at most
// kChildReadBudget from each pipe. what it reads is never logged
class ChildLogReader {
 public:
  explicit ChildLogReader(ChildLogTarget target);
  // reads what the pipes still hold without waiting, sends the lines they did not end and the drops, and joins
  ~ChildLogReader();

  ChildLogReader(const ChildLogReader&) = delete;
  ChildLogReader& operator=(const ChildLogReader&) = delete;

  // any thread: a child's read ends of stdout and stderr, non-blocking (either may be invalid), its lines under source,
  // a fixed token of static storage
  void add(std::string_view source, UniqueFd out, UniqueFd err);

 private:
  struct Child;
  struct Added {
    std::string_view source;
    UniqueFd out;
    UniqueFd err;
  };

  void run();

  ChildLogTarget target_;
  UniqueFd wake_;
  std::mutex mutex_;
  std::vector<Added> added_;
  bool stopping_ = false;
  std::thread thread_;
};

inline constexpr std::size_t kChildReadBudget = 64 * 1024;

}  // namespace cadence::audiod
