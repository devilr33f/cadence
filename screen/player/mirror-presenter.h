#pragma once

#include "player/mirror-schedule.h"

#include <condition_variable>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <span>
#include <string>
#include <thread>
#include <vector>

#if defined(__ANDROID__)
struct ANativeWindow;
#endif

namespace cadence::screen::player {

// a platform's video decoder for the mirror presenter, called on the presenter's thread alone. android's is
// MediaCodec onto the video SurfaceView (mirror-decoder-android.cpp); linux has none yet (the pi's v4l2m2m path is
// a follow-up), so a linux screen does not say it mirrors
class MirrorDecoder {
 public:
  virtual ~MirrorDecoder() = default;
  // the decoder made again for this config (annex b sps/pps, or vps/sps/pps); false when it cannot be (no surface,
  // the codec refused): the session's frames are skipped until the next config
  virtual bool configure(MirrorCodec codec, int width, int height, std::span<const std::uint8_t> config) = 0;
  enum class Queued {
    // the decoder has it
    Taken,
    // no input buffer free now: it is offered again
    Full,
    // the decoder cannot take any (android: the surface went with its codec, until it is back and a keyframe comes)
    Dropped,
  };
  // one access unit. ptsNs is the presenter's (small, from the session's start): a vendor codec that keeps its
  // timestamps in 90 kHz or 32 bits never sees a sender's ntp-epoch time
  virtual Queued queue(std::span<const std::uint8_t> data, std::int64_t ptsNs, bool keyframe) = 0;
  struct Output {
    std::int64_t handle = 0;
    std::int64_t ptsNs = 0;
  };
  // the outputs decoded since the last call, waiting up to waitNs for the first
  virtual void outputs(std::vector<Output>& out, std::int64_t waitNs) = 0;
  // an output shown at atNs (CLOCK_MONOTONIC; 0 = at once), or dropped
  virtual void release(std::int64_t handle, bool render, std::int64_t atNs) = 0;
  // the decoder goes (each output already released)
  virtual void close() = 0;
};

// the airplay mirror presenter: the tap's mirror frames and presentation clock (TapSinks.mirror, .clock, on the tap's
// thread) and the link's MirrorStart and MirrorStop (the main thread) drive a decoder on a thread of its own, by
// MirrorSchedule's rules. a frame's bytes are copied once into a bounded queue; an overflow drops the queue and waits
// for the next keyframe. nothing about a frame is logged but counts
class MirrorPresenter {
 public:
  using Clock = std::function<std::int64_t()>;
  // the presenter's thread starts at once; clock is CLOCK_MONOTONIC in ns (a test's fake)
  MirrorPresenter(std::unique_ptr<MirrorDecoder> decoder, Clock now = {});
  ~MirrorPresenter();
  MirrorPresenter(const MirrorPresenter&) = delete;
  MirrorPresenter& operator=(const MirrorPresenter&) = delete;

  // the platform's decoder, or null where there is none (the screen does not advertise mirroring)
  static std::unique_ptr<MirrorPresenter> create();

  // the tap's thread
  void frame(const std::string& sessionId, std::int64_t ptsNs, bool config, bool keyframe,
             std::span<const std::uint8_t> data);
  void clock(std::int64_t mediaNs, std::int64_t monoNs, std::int64_t latencyNs = 0);
  // the main thread: MirrorStart (codec 1 h264, 2 h265, 0 from the config) and MirrorStop
  void start(const std::string& sessionId, int codec, int width, int height);
  void stop(const std::string& sessionId);

  [[nodiscard]] bool active() const;

  struct Counts {
    std::uint64_t frames = 0;
    std::uint64_t configs = 0;
    std::uint64_t decoded = 0;
    std::uint64_t rendered = 0;
    std::uint64_t late = 0;
    std::uint64_t skipped = 0;
    std::uint64_t overflows = 0;
    std::uint64_t refused = 0;
    // units the decoder could not take (no codec on the surface)
    std::uint64_t dropped = 0;
    // shown at once against an anchored clock that could not be the video's (MirrorSchedule rule 6)
    std::uint64_t unsynced = 0;
    // released ahead of their lead, as kMaxHeld outputs were held already
    std::uint64_t early = 0;
  };
  [[nodiscard]] Counts counts() const;

  // the queue's bounds
  static constexpr std::size_t kMaxQueued = 90;
  static constexpr std::size_t kMaxQueuedBytes = 12u << 20;
  // outputs held for their time at most: each is a buffer the decoder cannot fill, and a decoder with all of them
  // held stops; beyond it the oldest goes to the surface with its release time (MediaCodec queues it there)
  static constexpr std::size_t kMaxHeld = 4;
  // a decoder's pts start here for each config (MirrorDecoder::queue)
  static constexpr std::int64_t kDecoderPtsStartNs = 1'000'000'000;
  // how often an open session says how it goes (mirror_stats)
  static constexpr std::int64_t kStatsEveryNs = 10'000'000'000;

 private:
  struct Frame {
    std::string session;
    std::int64_t ptsNs = 0;
    bool config = false;
    bool keyframe = false;
    std::vector<std::uint8_t> data;
  };
  void run();
  // the presenter's thread: every held output dropped, then the decoder closed
  void closeDecoder();
  // the counts so far with the held outputs and the clock (numbers only), taken under mu_ and logged outside it
  struct Stats {
    Counts counts;
    std::uint64_t held = 0;
    bool anchored = false;
    std::int64_t aheadNs = 0;
  };
  [[nodiscard]] Stats statsLocked() const;
  static void log(const char* event, const Stats& stats);

  std::unique_ptr<MirrorDecoder> decoder_;
  Clock now_;
  mutable std::mutex mu_;
  std::condition_variable cv_;
  MirrorSchedule schedule_;
  std::deque<Frame> queue_;
  std::size_t queuedBytes_ = 0;
  // the next frame the decoder had no room for
  std::optional<Frame> pending_;
  // the presenter's thread alone: outputs not yet due, and whether the decoder is configured
  std::vector<MirrorDecoder::Output> held_;
  bool open_ = false;
  // the sender's pts at the decoder's kDecoderPtsStartNs, set at the first unit after a config (-1: not yet)
  std::int64_t ptsBaseNs_ = -1;
  // the first output after a config was looked at (mirror_first_output)
  bool firstOutputSeen_ = false;
  std::int64_t nextStatsNs_ = 0;
  // the last output's release time against now, under mu_ (the stats line's ahead_ms)
  std::int64_t lastAheadNs_ = 0;
  bool closeRequested_ = false;
  bool stopping_ = false;
  // MirrorStart's codec and size for its session
  std::string startSession_;
  int startCodec_ = 0;
  int startWidth_ = 0;
  int startHeight_ = 0;
  Counts counts_;
  std::thread thread_;
};

#if defined(__ANDROID__)
// the activity's video surface as a window for MediaCodec (android-main.cpp: taken with ANativeWindow_fromSurface,
// whose reference this takes), null as it goes away: returns once no codec is left on it
void androidMirrorWindowChanged(ANativeWindow* window);
#endif

}  // namespace cadence::screen::player
