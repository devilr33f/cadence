#pragma once

#include "backend/pcm-backend.h"
#include "playback/alsa-playback.h"
#include "playback/jitter-buffer.h"
#include "playback/mixer.h"
#include "playback/thread-priority.h"
#include "result.h"
#include "trace/trace-queue.h"

#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <mutex>
#include <optional>
#include <span>
#include <stop_token>
#include <string>
#include <string_view>
#include <thread>

namespace cadence {

inline constexpr int kPlaybackSampleRate = 48000;
inline constexpr std::size_t kPlaybackChannels = 2;

// the sizes are the board's playback layout
struct AlsaPlayerConfig {
  std::size_t periodFrames;
  std::size_t bufferFrames;
  // between two periods and the buffer
  std::size_t maxDeviceFillFrames;
  JitterBufferConfig jitter;
  // the device's: 2, or 1 for a mono downmix
  std::size_t channels = kPlaybackChannels;
};

// counters since construction and the latest device fault; never audio
struct PlaybackStatus {
  bool running = false;
  // the voice source's queue
  JitterBufferStatus queue;
  // stereo frames handed to the device, silence included
  std::uint64_t frames = 0;
  // times audio waiting in the device was thrown away after a cancel, a newer response, or a stop
  std::uint64_t discards = 0;
  // frames those discards took back from the device before they were heard
  std::uint64_t rewoundFrames = 0;
  // device underruns and suspends the stream restarted after
  std::uint64_t recoveries = 0;
  std::uint64_t writeErrors = 0;
  // the first of the device writes that failed in a row and could not restart the stream; empty once a write
  // takes audio or the stream recovers
  std::optional<std::chrono::steady_clock::time_point> failingSince;
  std::optional<PlaybackError> lastError;
  // the playback thread's own, once it runs (android: raised to kNiceAudio)
  ThreadPriority priority;
};

// plays queued responses' 48 kHz mono audio, with music, through the pcm backend on a 48 kHz stereo (or mono)
// device from its own thread, writing silence whenever there is nothing to play and keeping at most
// maxDeviceFillFrames in the device. the voice is read through a Mixer whose voice source holds the responses and
// whose tone source holds the earcons and alarm tones played over them; the backend mixes them over the music
// inputs, ducking the music while the voice holds or either source sounds, and delays everything by its limiter's
// look-ahead. a cancel or a newer response that replaces one still arriving stops the older audio at once: before
// the call returns, queued audio is dropped and, when audio already read may belong to a stopped response, the
// playback thread has taken back what the device still held but the period already playing, at most one period,
// and what the backend still delays (the call waits at most 250 ms for that, which only a stuck device needs).
// cancelFrom takes the device back only when it may hold audio of a response it stops. shutting the gate takes it
// back the same way.
//
// begin, push, end, cancel, cancelFrom, the tone, gate and gain calls, the backend's settings, watchCompletion,
// idle, and status may be called from any thread; start and stop belong to one controlling thread. the callbacks
// (completion and viz observers) always run on the playback thread: they must post to the controller loop and never
// touch session state or wait on the controller.
class AlsaPlayer {
 public:
  // one settled watch: the response key and whether its last sample was actually heard
  using CompletionObserver = std::function<void(std::string key, bool success)>;
  using VizObserver = std::function<void(const dsp::VizFrame& frame)>;

  AlsaPlayer(PcmSink& sink, AlsaPlayerConfig config, PcmBackendInputs inputs = {});
  ~AlsaPlayer();

  AlsaPlayer(const AlsaPlayer&) = delete;
  AlsaPlayer& operator=(const AlsaPlayer&) = delete;

  // opens exact 48 kHz s16 playback of the configured channels and starts the playback thread
  Result<PlaybackFormat, PlaybackError> start();
  // also takes back what the device holds, drops everything queued, and retires every queued response
  void stop();

  // set before start, never while running
  void onCompletion(CompletionObserver observer);
  void onViz(VizObserver observer);
  // the voice's marks, by response id, from the playback thread through the tracer's queue: FIRST_SAMPLE_PLAYED once
  // a response's first sample was written to the device (buffered_ms: what the device held ahead of it), UNDERRUN
  // when it ran dry before its end (device 0) or the device recovered while it played (device 1)
  void traceTo(trace::Tracer tracer);

  // see JitterBuffer for the response rules
  bool begin(std::string_view responseId);
  PushResult push(std::string_view responseId, std::span<const std::int16_t> pcm);
  bool end(std::string_view responseId);
  void cancel(std::optional<std::string_view> responseId = std::nullopt);
  // stops that response and every one queued after it, like cancel, and the ones queued before it play on.
  // when the device already holds audio of a stopped one, all it holds is taken back, which can cut up to the
  // fill off the end of the one before. an id no longer queued is only retired
  void cancelFrom(std::string_view responseId);

  // plays pcm once on the tone source, mixed over the voice; it queues behind a tone still sounding. Stale for
  // a key used before, Overflow when the tone queue has no room for it
  PushResult playTone(std::string_view key, std::span<const std::int16_t> pcm);
  // stops every tone, a loop too, like cancel stops the voice
  void cancelTones();
  // plays period over and over on the tone source until stopLoop, gapless, mixed over the voice and the earcons:
  // an alarm's or a timer's ring. it replaces a loop already sounding
  void loopTone(std::span<const std::int16_t> period);
  // loops what feed gives instead (a ring of segments), replacing a loop already sounding
  void loopFeed(std::shared_ptr<LoopFeed> feed);
  // stops the loop and, before it returns, takes it back from the device like cancelTones; false when nothing looped
  bool stopLoop();
  // the software volume over the whole mix, after the limiter; clamped to 0..1
  void setMasterGain(float gain);
  // the music inputs mix only while the gate is open (rule 7). shutting it clears them and, before it returns,
  // takes back what the device holds but the period playing, voice included, like a cancel
  void setGateOpen(bool open);
  // duck, eq and viz settings
  [[nodiscard]] PcmBackend& backend() { return backend_; }

  // arms the single audible-completion watch for a response begin() already took. false when not running,
  // another watch is still armed, or the response already retired (its boundary can never be observed).
  // exactly one observer call follows an armed watch: success only once the response's last frame was
  // written and the device-delay watermark shows it played in the same device generation; failure on a
  // cancel or replacement, a stream recovery, a write failure, or a failed delay observation. an empty or
  // failed watch never guesses from queue drain
  bool watchCompletion(std::string_view responseId);
  // a synchronized snapshot of the playback thread's watermarks: true only while no voice response is
  // active or queued, no voice frame is read but unwritten, and the last submitted voice boundary has
  // passed the device-delay watermark. an unavailable delay measurement is not idle. readiness only;
  // ordinary playback and follow-up timing are unchanged
  [[nodiscard]] bool idle() const;

  [[nodiscard]] PlaybackStatus status() const;

 private:
  void playLoop(std::stop_token stop);
  // takes back what the device has not played and stops the stream; a drop alone leaves dmix playing what
  // it already mixed
  void discardDevice();
  // gives the playback thread a bounded time to throw away device audio older than this generation
  void awaitDiscard(std::uint64_t generation);
  // playback thread: settles the armed watch, if any, through the observer
  void finishWatch(bool success);
  template <typename Update>
  void record(Update update);

  PcmSink& sink_;
  const AlsaPlayerConfig config_;
  Mixer mixer_;
  PcmBackend backend_;
  VizObserver vizObserver_;
  trace::Tracer tracer_;

  // set by start before the thread runs
  PlaybackFormat format_;

  // the generation whose audio the device holds, published by the playback thread
  std::mutex discardMutex_;
  std::condition_variable discarded_;
  std::uint64_t deviceGeneration_ = 0;
  // false once the playback thread has exited, so nobody waits on it
  bool threadRunning_ = false;

  // one movable watched key; the playback thread adds the absolute write boundary once its last frame is
  // written, so nothing is allocated per playback period
  struct Watch {
    std::string key;
    std::uint64_t generation = 0;
    std::optional<std::uint64_t> marker;
  };
  std::mutex watchMutex_;
  // guarded by watchMutex_: start sets it before the thread runs, the playback thread clears it on exit
  // before settling any armed watch as failure, so an arm can never slip past the exit settlement
  bool running_ = false;
  std::optional<Watch> watch_;
  CompletionObserver observer_;
  // exact watched-key cancels can arrive after the response already left the queue, where the jitter
  // generation no longer moves. the effective device generation is mixer generation + this bias, so such a
  // cancel still discards device audio and fails the watch. read on any thread, bumped under watchMutex_
  std::atomic<std::uint64_t> discardBias_{0};

  // the playback thread's watermarks, published for the controller's idle()
  struct IdleSnapshot {
    std::uint64_t written = 0;
    std::uint64_t voiceBoundary = 0;
    std::uint64_t delay = 0;
    bool delayKnown = false;
    bool queueEmpty = true;
    bool voicePending = false;
    bool running = false;
  };
  mutable std::mutex idleMutex_;
  IdleSnapshot idle_;

  mutable std::mutex statusMutex_;
  PlaybackStatus status_;

  std::jthread thread_;
};

}  // namespace cadence
