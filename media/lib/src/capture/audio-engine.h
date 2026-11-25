#pragma once

#include "audio/stock-api.h"
#include "capture/alsa-capture.h"
#include "capture/frame-buffer.h"
#include "capture/mute-state.h"
#include "capture/playback-monitor.h"
#include "result.h"

#include <array>
#include <atomic>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <span>
#include <stop_token>
#include <string>
#include <thread>
#include <vector>

namespace cadence {

struct AudioEngineConfig {
  // vqe preset, stock paths, and the board's capture layout, whose period and buffer capture asks for
  StockAudioConfig stock;
};

struct AudioEngineInfo {
  StockAudioInfo stock;
  CaptureFormat capture;
};

enum class AudioEngineErrorCode {
  AlreadyRunning,
  StockFailed,
  CaptureOpenFailed,
  CaptureFormatMismatch,
  CaptureFailed,
  VqeFailed,
  SpotterFailed,
};

// detail is a bounded stock or capture error description; never audio
struct AudioEngineError {
  AudioEngineErrorCode code;
  std::string detail;
};

// bounded single-line text, safe to log
std::string describe(const AudioEngineError& error);

// counters since start and the latest fault; never audio
struct AudioEngineStatus {
  bool running = false;
  bool streaming = false;
  bool muted = false;
  // vqe chunks handled, including those vqe failed on
  std::uint64_t chunks = 0;
  std::uint64_t wakes = 0;
  // wake hits ignored because the microphone was muted
  std::uint64_t suppressedWakes = 0;
  // command hits heard, whether or not anyone listens, and those ignored because the microphone was muted
  std::uint64_t commandHits = 0;
  std::uint64_t suppressedCommandHits = 0;
  // frames delivered to onPcm16
  std::uint64_t frames = 0;
  // capture discontinuities: overruns, failed reads, and fifo overflow
  std::uint64_t gaps = 0;
  // frames of partial vqe chunks thrown away at a gap
  std::uint64_t discardedFrames = 0;
  // oldest frames the one-second fifo dropped while processing fell behind
  std::uint64_t overflowFrames = 0;
  std::uint64_t recoveries = 0;
  std::uint64_t captureErrors = 0;
  // the first of the capture reads that failed in a row and could not restart the stream; empty once a read
  // returns audio or the stream recovers
  std::optional<std::chrono::steady_clock::time_point> captureFailingSince;
  std::uint64_t vqeErrors = 0;
  // includes every chunk polled while the spotter is unavailable
  std::uint64_t spotterErrors = 0;
  // every spotter_get_next_event code seen, by code, whether or not it made a wake
  std::array<std::uint64_t, 10> spotterEvents{};
  // confirmations the combined spotter credited to neither alice nor the command words
  std::uint64_t unknownComponents = 0;
  std::uint64_t spotterRebuilds = 0;
  std::uint64_t spotterRebuildFailures = 0;
  std::optional<AudioEngineError> lastError;
  // the playback monitor configured its digital speaker references this start
  bool playbackMonitorAvailable = false;
};

struct AudioEngineCallbacks {
  // a wake word started streaming
  std::function<void()> onWake;
  // one 20 ms frame of 16 kHz mono stt audio (vqe omni with gain)
  std::function<void(std::span<const std::int16_t>)> onPcm16;
  // a command word was heard; it never starts streaming. the phrase is what the owner said: never log it
  std::function<void(std::string phrase)> onCommand;
  // 20 ms music levels measured from the digital speaker references on the capture thread: derived energy
  // only, never audio and never microphones. absent unless the caller wants the playback monitor at all
  std::function<void(MusicLevels)> onPlaybackLevels;
};

// captures on one thread and runs vqe, the wake spotter, and framing on a second. microphone audio
// reaches onPcm16 only while streaming: from a wake hit or setStreaming(true) until setStreaming(false)
// or either source makes the effective mute true, and never while muted. mute acts at capture, so vqe and the
// spotter never hear muted audio.
// framing restarts only when streaming turns on: a wake or setStreaming(true) while streaming keeps the
// stream contiguous, and turning streaming off drops the partial frame. a command hit only reaches
// onCommand, when it is set and the microphone is not muted.
//
// every callback runs on the processing thread with the engine's gate lock held, so once setStreaming(false)
// or a mute setter returns with effectiveMuted true no frame is being or will be delivered. those callbacks:
// - must return quickly and never block or wait on another thread; post real work to a queue, because a
//   setter waits for a running callback and vqe stalls while one runs;
// - may call setStreaming, either mute setter, muteState, and status, but must not throw or call start or stop.
//
// onPlaybackLevels is different: it runs on the capture thread, with no engine lock held, and once more
// synchronously after capture has joined on stop. it must not block, throw, or call any engine setter, and
// it never receives raw audio, only derived levels, so metering continues while vqe and the spotter are
// stalled and while the microphone is muted.
// callers must never invoke setStreaming or either mute setter while holding a lock their callbacks also take.
// start and stop belong to one controlling thread.
class AudioEngine final : public MuteControl {
 public:
  AudioEngine(PcmSource& source, AudioEngineCallbacks callbacks, StockLibraryLoader loader = openStockLibrary);
  ~AudioEngine() override;

  AudioEngine(const AudioEngine&) = delete;
  AudioEngine& operator=(const AudioEngine&) = delete;

  // starts the stock engines and exact-format capture; a spotter failure is reported, not fatal
  Result<AudioEngineInfo, AudioEngineError> start(const AudioEngineConfig& config);
  void stop();

  // false drops any partial frame; true is ignored while muted
  void setStreaming(bool enabled);
  // changes only the physical or software-privacy source. either effective mute takes effect before returning:
  // streaming stops, queued capture and partial chunks and frames are dropped, and capture is fed to vqe as
  // silence. after the effective OR becomes unmuted one device buffer more stays silent, since it may hold audio
  // captured while muted. clearing one source never resumes streaming and never overrides the other source.
  // generation increments on either source change, so delayed publications cannot undo a newer privacy decision.
  // an effective unmute starts the wake and command warm-ups again, as a capture (re)open does
  MuteState setPhysicalMuted(bool muted) override;
  MuteState setPrivacyMuted(bool muted) override;
  [[nodiscard]] MuteState muteState() const override;
  [[nodiscard]] AudioEngineStatus status() const;

  // a group calibration's recording: every microphone of the capture layout (its speaker references left out, at
  // most kMaxRawChannels), interleaved in the layout's order, as captured (before the vqe, whose echo canceller would
  // remove the device's own chirp), at the capture's rate, one call per vqe chunk on the processing thread with the
  // gate held, while the microphone is not muted; until a tap of none. the same rules as the callbacks: it must
  // return quickly and never block
  static constexpr std::size_t kMaxRawChannels = 8;
  using RawTap = std::function<void(std::span<const std::int16_t> interleaved, std::size_t channels)>;
  void setRawTap(RawTap tap);
  // the capture's rate, once start returned
  [[nodiscard]] int captureRate() const { return info_.capture.sampleRate; }
  // the microphones the raw tap carries, once start returned
  [[nodiscard]] std::size_t rawChannels() const { return rawChannels_.size(); }

 private:
  void captureLoop(std::stop_token stop);
  void processLoop(std::stop_token stop);
  void processChunk();
  // the chunk's microphones to the raw tap, if one is set
  void tapRaw();
  void deliver(std::span<const std::int16_t> samples);
  void pollHits();
  void activate();
  void command(const std::string& phrase);
  MuteState setMuteSource(bool physical, bool muted);
  void fault(AudioEngineErrorCode code, std::string detail, std::uint64_t AudioEngineStatus::*counter);
  // measures the speaker references of a successful read and publishes any completed window
  void publishPlaybackLevels(std::span<const std::int16_t> samples);
  // resets the monitor and publishes zero: a gap, a stop, or broken filter state
  void silencePlaybackLevels();
  template <typename Update>
  void record(Update update);

  PcmSource& source_;
  const AudioEngineCallbacks callbacks_;
  StockAudio audio_;

  // set by start before the threads run
  AudioEngineInfo info_;
  std::unique_ptr<CaptureFifo> fifo_;
  std::vector<std::int16_t> readBuffer_;
  std::vector<std::int16_t> chunk_;
  std::size_t assembledFrames_ = 0;
  // the raw tap's channels in chunk_, and its samples (the processing thread's)
  std::vector<std::size_t> rawChannels_;
  std::vector<std::int16_t> raw_;

  // measures the digital speaker references of each successful capture read; capture thread only
  PlaybackMonitor playbackMonitor_;
  // true between a start that armed the monitor and the stop that disarms it
  bool playbackMonitoring_ = false;

  // recursive so callbacks can call the setters
  mutable std::recursive_mutex gate_;
  bool streaming_ = false;
  RawTap rawTap_;
  MuteState muteState_;
  PcmFramer framer_;

  // pairs mute changes with capture pushes, so no audio read across a mute change reaches the fifo unsilenced
  std::mutex captureMutex_;
  // odd while muted; bumped on every change
  std::uint64_t muteEpoch_ = 0;
  // set by an effective unmute, taken by the processing thread before its next chunk
  std::atomic<bool> rearmWake_{false};

  // never held while a callback runs
  mutable std::mutex statusMutex_;
  AudioEngineStatus status_;

  std::jthread processing_;
  std::jthread capture_;
};

}  // namespace cadence
