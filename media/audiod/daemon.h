#pragma once

#include "audiod/ipc.h"
#include "audiod/log.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <cstdint>
#include <memory>
#include <optional>
#include <string_view>
#include <vector>

namespace cadence::audiod {

using Backend = cadence_audiod_v1_Backend;
using Configure = cadence_audiod_v1_Configure;

inline constexpr std::string_view kVersion = CADENCE_AUDIOD_VERSION;
static_assert(kVersion.size() < sizeof(cadence_audiod_v1_Ready::version), "CADENCE_AUDIOD_VERSION is too long");
static_assert(kVersion.size() < sizeof(cadence_audiod_v1_HealthPong::version), "CADENCE_AUDIOD_VERSION is too long");

// the backends this build carries
std::vector<Backend> compiledBackends();

// one of the daemon's parts (playback, capture): it registers its own handlers, rebuilds from each Configure
// and stops once cadenced is gone. called on the loop thread
class Part {
 public:
  virtual ~Part() = default;
  // a configuration the daemon accepted, before Ready goes out
  virtual void configure(const Configure& configuration) = 0;
  // what the part adds to that configuration's Ready, before it goes out
  virtual void fillReady(cadence_audiod_v1_Ready& ready) { (void)ready; }
  // that configuration's Ready went out
  virtual void ready() {}
  // cadenced is gone: stop, close what the configuration opened and forget it, so nothing keeps playing or
  // capturing for a dead client (rule 7)
  virtual void reset() = 0;
};

// the composition root of audiod's messages: Configure -> Ready and HealthPing -> HealthPong. the parts hang
// off it, and are destroyed in the reverse order they were added, so a part may use one added before it. destroy
// it only after the loop stopped
class Daemon {
 public:
  Daemon(IpcServer& ipc, const Log& log, std::vector<Backend> backends);
  ~Daemon();

  Daemon(const Daemon&) = delete;
  Daemon& operator=(const Daemon&) = delete;

  // before the loop runs; the daemon owns its parts
  void add(std::unique_ptr<Part> part);

  // the configuration Ready answered last, until the client is gone
  [[nodiscard]] const std::optional<Configure>& configuration() const { return configuration_; }

 private:
  void configure(const Configure& request);
  void clientGone();
  void pong(std::uint64_t seq);
  [[nodiscard]] bool carries(Backend backend) const;

  IpcServer& ipc_;
  const Log& log_;
  std::vector<Backend> backends_;
  std::optional<Configure> configuration_;
  std::vector<std::unique_ptr<Part>> parts_;
};

}  // namespace cadence::audiod
