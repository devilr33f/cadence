#pragma once

#include "sync/clock-estimator.h"
#include "sync/clock-map.h"
#include "sync/clock-socket.h"
#include "sync/sync-clock.h"

#include <atomic>
#include <cstdint>
#include <functional>
#include <mutex>
#include <optional>
#include <thread>
#include <vector>

namespace cadence::sync {

struct ClockClientConfig {
  // between probes: 10 Hz is ~480 B/s a member, and the spike's precision was measured at it
  std::int64_t intervalNs = 100'000'000;
  // a reply whose t1 is older than this is dropped: the exchange it closes was not one wire trip
  std::int64_t maxRttNs = 1'000'000'000;
  // no valid reply for this long: the mapping is withdrawn (the playout mutes) and the window starts over
  std::int64_t lostAfterNs = 3'000'000'000;
  ClockEstimatorConfig estimator;
  ClockSmootherConfig smoother;
  // the clock t1 and t4 speak (the sim's skewed one): the mapping is from it to the leader's
  SyncClock clock;
};

struct ClockClientStats {
  std::uint64_t sent = 0;
  std::uint64_t replies = 0;
  // a tag the key did not make, a reply to nobody's probe, one too late
  std::uint64_t refused = 0;
  std::uint64_t losses = 0;
  bool kernelStamps = false;
  std::optional<ClockEstimate> last;
  // how far the mapping's last step jumped, 0 before one
  std::int64_t lastStepNs = 0;
};

// the member's half: probes the leader on a thread of its own, fits the window and publishes the smoothed mapping to
// mapping(), which the playback thread reads without waiting. onThread runs first on the thread
class ClockClient {
 public:
  ClockClient(ClockSocket socket, ClockPeer leader, std::vector<std::uint8_t> key, ClockClientConfig config = {},
              std::function<void()> onThread = {});
  ClockClient(const ClockClient&) = delete;
  ClockClient& operator=(const ClockClient&) = delete;
  ~ClockClient();

  void start();
  void stop();
  [[nodiscard]] const SharedClockMapping& mapping() const { return mapping_; }
  [[nodiscard]] ClockClientStats stats() const;

 private:
  void run();
  void probe(std::int64_t now);
  void received(const ClockDatagram& d, std::span<const std::uint8_t> packet);

  ClockSocket socket_;
  ClockPeer leader_;
  std::vector<std::uint8_t> key_;
  ClockClientConfig config_;
  std::function<void()> onThread_;
  ClockEstimator estimator_;
  ClockSmoother smoother_;
  SharedClockMapping mapping_;

  std::thread thread_;
  std::atomic<bool> stop_{false};
  std::uint32_t seq_ = 0;
  // seqs sent, to refuse a duplicate or an answer to nobody's probe (a ring of the last ones)
  std::vector<std::uint32_t> pending_;
  std::int64_t lastReplyNs_ = 0;

  mutable std::mutex statsMutex_;
  ClockClientStats stats_;
};

}  // namespace cadence::sync
