#pragma once

#include "audiod/log.h"

#include <memory>
#include <string>

namespace cadence::audiod {

// audiod in a process of its own host (the android app's service, which an app cannot exec audiod beside, S2): the
// same daemon cadence-audiod runs, on a thread of its own, serving the socket cadenced dials (an abstract @name on
// android). no signal handling: the host stops it. its log lines go to the host's sink, one json line each
class EmbeddedAudiod {
 public:
  // listens and starts the daemon's thread; null, logged to sink, when the loop or the socket cannot be made
  static std::unique_ptr<EmbeddedAudiod> start(const std::string& socket, Log::Sink sink);
  // stops the daemon and waits for its thread
  ~EmbeddedAudiod();

  EmbeddedAudiod(const EmbeddedAudiod&) = delete;
  EmbeddedAudiod& operator=(const EmbeddedAudiod&) = delete;

  // false once the daemon's loop has ended by itself (a failed epoll): the host starts another
  [[nodiscard]] bool running() const;

 private:
  struct State;
  explicit EmbeddedAudiod(std::unique_ptr<State> state);
  std::unique_ptr<State> state_;
};

}  // namespace cadence::audiod
