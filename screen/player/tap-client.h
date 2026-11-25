#pragma once

#include "player/ring-offer.h"
#include "player/video-follow.h"

#include <array>
#include <atomic>
#include <cstdint>
#include <functional>
#include <mutex>
#include <optional>
#include <span>
#include <string>
#include <thread>
#include <vector>

namespace cadence::screen::player {

// what the tap's frames feed (audiod.proto's TapEnvelope). pcm and bands are called on the tap's own thread and must
// never wait (the visualizer's PcmTimeline only try-locks); rings are queued for the main thread
struct TapSinks {
  // a PcmSnapshot: interleaved f32 of `channels`, its first frame heard at monoNs (CLOCK_MONOTONIC, 0 = not known yet)
  std::function<void(std::span<const float> interleaved, std::uint32_t channels, std::uint32_t rate,
                     std::int64_t monoNs)>
      pcm;
  // a VizFrame's 16 bands, 0-255 each
  std::function<void(const std::array<std::uint8_t, 16>& bands)> bands;
  // a RingOffer arrived and waits in takeRings(); the app wakes its loop
  std::function<void()> ringReady;
  // an airplay mirror frame (annex b nals at pts on the presentation clock's timeline), for the mirror presenter
  struct MirrorFrame {
    std::string sessionId;
    std::int64_t ptsNs = 0;
    bool config = false;
    bool keyframe = false;
    std::span<const std::uint8_t> data;
  };
  std::function<void(const MirrorFrame& frame)> mirror;
  // the presentation clock: media_ns sounding at mono_ns
  std::function<void(std::int64_t mediaNs, std::int64_t monoNs, std::int64_t latencyNs)> clock;
  // a multiroom group's media clock (GroupClock), which a follower's picture follows (multiroom P4)
  std::function<void(const FollowClock& clock)> groupClock;
};

enum class TapFrame { Pcm, Bands, Clock, Mirror, Ring, GroupClock, Refused };

// joins a mirror access unit that came as fragments (MirrorFrame's seq, offset and total_size) back into the unit:
// each fragment must be the next of the unit it waits for, or the unit is dropped and the next one starts afresh (the
// mirror presenter then waits for a keyframe, as after any loss). one session's units arrive in order on one socket
class MirrorJoin {
 public:
  // a unit larger than this is refused
  static constexpr std::size_t kMaxUnit = 8 * 1024 * 1024;

  // the whole unit once its last fragment came; nullopt meanwhile, and for a fragment that breaks the unit
  std::optional<std::vector<std::uint8_t>> add(std::uint32_t seq, std::uint32_t offset, std::uint32_t total,
                                               std::span<const std::uint8_t> data);
  // units given up on (a fragment lost, out of order, or too large)
  [[nodiscard]] std::uint64_t dropped() const { return dropped_; }

 private:
  void drop();

  bool active_ = false;
  std::uint32_t seq_ = 0;
  std::uint32_t total_ = 0;
  std::vector<std::uint8_t> unit_;
  std::uint64_t dropped_ = 0;
};

// one tap frame and the fds that came with it: decoded, the fds taken (a frame that is not a ring offer has its fds
// closed), handed to the sinks. a ring offer comes back in ring; error holds the refusal's token
// fragments of a mirror unit go through join; without one they are refused
TapFrame dispatchTapFrame(std::span<const std::uint8_t> frame, std::span<const int> fds, const TapSinks& sinks,
                          std::optional<RingOffer>& ring, std::string* error, MirrorJoin* join = nullptr);

// the screen's client of audiod's tap (Ready.tap_path: a SOCK_SEQPACKET socket, an abstract name when it starts
// with @). a thread of its own connects, reads every frame with its SCM_RIGHTS fds, feeds the visualizer's pcm and
// bands, and queues each ring offer (a MIXER session's ao_cadence ring) for the main thread, which hands it to mpv.
// a lost or refused connection is retried with ipc's backoff; nothing is ever written to audiod, and no pcm is kept
// past the sinks (rule 8)
class TapClient {
 public:
  explicit TapClient(TapSinks sinks);
  ~TapClient();
  TapClient(const TapClient&) = delete;
  TapClient& operator=(const TapClient&) = delete;

  // connects (and keeps reconnecting) to path; another path moves it, an empty one stops
  void connect(const std::string& path);
  // reads an already connected socket (tests: one end of a socketpair), taking it; returns at once
  void adopt(int fd);
  void stop();

  // the offers received since the last call, oldest first (main thread)
  std::vector<RingOffer> takeRings();

  struct Counts {
    std::uint64_t frames = 0;
    std::uint64_t pcm = 0;
    std::uint64_t bands = 0;
    std::uint64_t rings = 0;
    std::uint64_t refused = 0;
  std::uint64_t mirror = 0;
    std::uint64_t connects = 0;
  };
  [[nodiscard]] Counts counts() const;
  [[nodiscard]] bool connected() const { return fd_.load() >= 0; }

 private:
  void run(std::string path, int adopted);
  // reads frames from fd until it closes or stop; false when stopped
  bool serve(int fd);

  TapSinks sinks_;
  // the reader thread's alone
  MirrorJoin join_;
  std::thread thread_;
  std::atomic<bool> stopping_{false};
  std::atomic<int> fd_{-1};
  std::string path_;
  mutable std::mutex mu_;
  std::vector<RingOffer> rings_;
  Counts counts_;
};

}  // namespace cadence::screen::player
