#pragma once

#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/sys.h"

#include <memory>
#include <optional>

namespace cadence::audiod {

// stops the loop on SIGTERM or SIGINT, read from a signalfd so the stop runs on the loop thread. install it
// before any thread starts: it blocks both signals for the process, and a child audiod spawns must unblock
// them. SIGPIPE is ignored, so a closed stderr cannot kill the daemon
class SignalStop {
 public:
  // nullptr, logged, when the signals cannot be routed to the loop
  static std::unique_ptr<SignalStop> install(Loop& loop, const Log& log);
  ~SignalStop();

  SignalStop(const SignalStop&) = delete;
  SignalStop& operator=(const SignalStop&) = delete;

 private:
  SignalStop(Loop& loop, const Log& log, UniqueFd fd);

  void onSignal();

  Loop& loop_;
  const Log& log_;
  UniqueFd fd_;
  std::optional<Loop::WatchId> watch_;
};

}  // namespace cadence::audiod
