#pragma once

#include "audio/opus-codec.h"
#include "audiod/command-words.h"
#include "capture/audio-engine.h"
#include "result.h"

#include <atomic>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <mutex>
#include <span>
#include <string>
#include <vector>

namespace cadence::audiod {

// the vqe output kept while it streams with no session open, for a session that opens right after a wake: 300 ms
inline constexpr std::size_t kPrerollFrames = 15;

// where the engine's events go. each runs on the engine's processing thread with its lock held, so it only posts;
// times are on the device clock: a wake's in unix ns, read on the processing thread as the spotter's hit came out of
// the chunk that held it (not when the loop sends it), a command's in unix ms
struct EngineEvents {
  std::function<void(std::int64_t atUnixNs)> wake;
  std::function<void(std::uint32_t id, std::int64_t atMs)> command;
  // a command phrase none of the configured words matched
  std::function<void()> unknownCommand;
  // one 20 ms packet of the vqe's 16 kHz output for the open session
  std::function<void(std::uint64_t session, std::vector<std::uint8_t> opus)> packet;
  // the encoder kept failing: nothing more goes out for this session
  std::function<void(std::uint64_t session)> failed;
  // while listening (setListen) and no session is open: each 20 ms of the vqe's 16 kHz output for the custom wake word,
  // seq counting them, at_ms when it came out (unix ms), gap after a mute or a session
  std::function<void(std::uint64_t seq, std::int64_t atMs, std::vector<std::int16_t> pcm, bool gap)> pcm;
};

// the board's vendor engines on a capture that stays open: v1's AudioEngine (the vqe with the board's preset, the
// combined spotter) from start to destruction. the spotter's hits are events whether or not a session is open; the
// vqe's output (omni with gain) is encoded into 24 kbps voip packets only for an open session. a wake streams the vqe
// output at once, and with no session open its last kPrerollFrames are kept: a session opened with preroll sends them
// first, as v1 streamed from the wake itself; a mute or a closed session forgets them. a command hit leaves as its
// index in the configured words, a phrase none matches as unknownCommand, its text never logged. the mute switch acts
// at capture (wake and command hits suppressed, no packets) and its unmute starts the warm-up again; a session still
// open then streams on. start blocks and may run on any thread; the rest is the loop thread's
class CaptureEngine {
 public:
  CaptureEngine(std::unique_ptr<PcmSource> source, EngineEvents events, CommandWords words, StockLibraryLoader loader);
  ~CaptureEngine();

  CaptureEngine(const CaptureEngine&) = delete;
  CaptureEngine& operator=(const CaptureEngine&) = delete;

  Result<AudioEngineInfo, AudioEngineError> start(const AudioEngineConfig& config);

  void setMuted(bool muted);
  // packets go out for session (non-zero), the kept vqe output first when preroll: all of it, or with prerollFromMs
  // (unix ms) only what came out from then on
  void open(std::uint64_t session, bool preroll, std::int64_t prerollFromMs = 0);
  void close();
  // the custom wake word listens: the vqe output streams with no session open, and goes out as pcm events
  void setListen(bool listen);
  // a group calibration's recording (in memory only, rule 8): the capture's microphones before the vqe (its echo
  // canceller would take out the device's own chirp), interleaved, tapChannels of them, at tapRate, reach tap a vqe
  // chunk at a time on the processing thread with its lock held, with that thread's CLOCK_MONOTONIC, until a tap of
  // none. nothing reaches it while muted
  using Tap = std::function<void(std::span<const std::int16_t> interleaved, std::size_t channels, std::int64_t monoNs)>;
  void setTap(Tap tap);
  [[nodiscard]] int tapRate() const { return engine_.captureRate(); }
  [[nodiscard]] std::size_t tapChannels() const { return engine_.rawChannels(); }

  [[nodiscard]] AudioEngineStatus status() const { return engine_.status(); }

 private:
  // the processing thread's
  void frame(std::span<const std::int16_t> pcm);
  void encode(std::uint64_t session, std::span<const std::int16_t> pcm);
  void command(const std::string& phrase);

  std::unique_ptr<PcmSource> source_;
  EngineEvents events_;
  CommandWords words_;
  std::atomic<std::uint64_t> session_{0};
  // the session that takes the kept output first
  std::atomic<std::uint64_t> prerollFor_{0};
  // a mute or a close: the processing thread forgets the kept output before its next frame
  std::atomic<bool> forget_{false};
  std::atomic<std::int64_t> prerollFrom_{0};
  std::atomic<bool> listen_{false};
  // the processing thread's: the kept output with when each frame came out, and the encoder of the session it encodes
  // for, fresh for each
  std::deque<std::vector<std::int16_t>> preroll_;
  std::deque<std::int64_t> prerollAt_;
  std::uint64_t listened_ = 0;
  bool listenGap_ = true;
  std::uint64_t encoding_ = 0;
  std::unique_ptr<OpusEncoder> encoder_;
  std::uint64_t encodeStreak_ = 0;
  bool encodeFailed_ = false;
  // last, so it stops before anything its callbacks use goes
  AudioEngine engine_;
};

}  // namespace cadence::audiod
