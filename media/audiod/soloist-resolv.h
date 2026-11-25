// soloist's resolv socket (media/lib/src/resolv/resolv-wire.h): the preload in its runtime forwards each getaddrinfo
// here, and this answers with audiod's own resolver — bionic's, on android, which asks netd, so whatever the network
// holds (a vpn, a private dns) resolves for soloist too. one thread of its own: a lookup blocks for as long as the
// system resolver holds it, which the loop's thread must never wait out
#pragma once

#include "audiod/log.h"
#include "audiod/sys.h"

#include <memory>
#include <string>

namespace cadence::audiod {

class SoloistResolv {
 public:
  // listens at path, unlinks a stale socket, and starts the thread; null, logged (soloist_resolv_failed call=…
  // errno=…), when it cannot
  static std::unique_ptr<SoloistResolv> listen(const Log& log, const std::string& path);

  // stops the thread and closes the socket; the socket file is left for the next one, like the pcm's
  ~SoloistResolv();

  SoloistResolv(const SoloistResolv&) = delete;
  SoloistResolv& operator=(const SoloistResolv&) = delete;

 private:
  struct State;
  explicit SoloistResolv(std::unique_ptr<State> state);
  std::unique_ptr<State> state_;
};

}  // namespace cadence::audiod
