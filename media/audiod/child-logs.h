#pragma once

#include "audiod/child-log.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"

#include <cstddef>
#include <memory>

namespace cadence::audiod {

// the children's lines over the socket (M6b-D7). LogsSet sets the switch every child's ChildLog reads (on until the
// first one). the lines queue here from the threads that read the children, at most kChildLinesQueued (past that the
// oldest is dropped), and go out as LogLine on the loop thread; one the socket refuses is dropped too. the drops are
// counted and logged as child_log_dropped at most once a second. register it before the loop runs
class ChildLogs {
 public:
  ChildLogs(Loop& loop, IpcServer& ipc, const Log& log);
  ~ChildLogs();

  ChildLogs(const ChildLogs&) = delete;
  ChildLogs& operator=(const ChildLogs&) = delete;

  // for the parts that run children: it stays usable after this is gone, and forwards nothing then
  [[nodiscard]] ChildLogTarget target() const;

 private:
  class Outbox;
  std::shared_ptr<Outbox> outbox_;
  std::shared_ptr<ChildLogSwitch> forward_;
};

inline constexpr std::size_t kChildLinesQueued = 512;

}  // namespace cadence::audiod
