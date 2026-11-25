#pragma once

#include "audio-layout.h"
#include "audio/opus-codec.h"
#include "audiod/keyword-model.h"
#include "audiod/log.h"
#include "audiod/mic-layout.h"
#include "capture/alsa-capture.h"
#include "kws/spotter.h"

#include <array>
#include <atomic>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <span>
#include <stop_token>
#include <string_view>
#include <thread>
#include <vector>

namespace cadence::audiod {

// model calls in a row that fail before the engine stops (about 2 s)
inline constexpr std::uint64_t kKeywordFailureLimit = 25;
// model calls before kws_started says what they took
inline constexpr std::size_t kKeywordStartedCalls = 50;

// where the keyword capture's events go. each runs on one of its threads and only posts; times are unix ms on the
// device clock
struct KeywordEvents {
  // the spotter's detection: the firing call's score, when the audio that completed it was read, and whether a
  // session was open then (a turn's audio, whose detection may arrive once the session has closed)
  std::function<void(float score, std::int64_t atMs, bool inSession)> wake;
  // one 20 ms packet of the open session
  std::function<void(std::uint64_t session, std::vector<std::uint8_t> opus)> packet;
  // the encoder kept failing: nothing more goes out for this session
  std::function<void(std::uint64_t session)> failed;
  // the model kept failing (invoke_failed): the spotter stopped, the capture still runs until the engine goes
  std::function<void(std::string_view code)> stopped;
  // while listening (setListen) and no session is open: each frame for the custom wake word, as ListenEvents::pcm
  std::function<void(std::uint64_t seq, std::int64_t atMs, std::vector<std::int16_t> pcm, bool gap)> pcm;
};

// what a keyword capture counted, read once it stopped
struct KeywordStats {
  std::uint64_t calls = 0;
  std::uint64_t failedCalls = 0;
  std::uint64_t wakes = 0;
  std::uint64_t droppedFrames = 0;
  std::uint64_t recoveries = 0;
  std::uint64_t readErrors = 0;
};

// the board's keyword model on a capture that stays open (the vk capsula mini's «Маруся»). a reader thread takes the
// device's frames, the layout's microphones mixed to 16 kHz mono, 20 ms at a time: an open session's are encoded into
// 24 kbps voip packets, else the last kPrerollFrames are kept; every frame also goes to the spotter's thread through
// a queue of kKeywordQueueFrames (queueFrames), so a slow model call never holds the device. a full queue drops its oldest frames,
// counts them, re-arms the spotter and logs once per burst (kws_frames_dropped). the spotter runs the model every
// 80 ms and its detections are events, a session open or not (holding them off is the caller's); a session opened with
// preroll first sends the kept frames read after the last detection. the mute switch lets nothing reach the
// spotter, the kept frames or a session, and the device's buffer that follows an unmute is silenced, as the vendor
// engines do; the first frame after a mute, a gap or a closed session re-arms the spotter (its warm-up, the model's
// state kept). kws_started logs the arena and the median of the first kKeywordStartedCalls calls; kKeywordFailureLimit
// failed calls in a row stop the spotter (kws_failed, then stopped). start, stop and the rest are the loop thread's
class KeywordCapture {
 public:
  KeywordCapture(std::unique_ptr<PcmSource> source, KeywordSetup setup, KeywordEvents events, const Log& log,
                 std::size_t queueFrames = kKeywordQueueFrames);
  ~KeywordCapture();

  KeywordCapture(const KeywordCapture&) = delete;
  KeywordCapture& operator=(const KeywordCapture&) = delete;

  // opens the device in the layout's format and starts both threads. the error is a token: format, open or thread
  std::optional<std::string_view> start(const CaptureLayout& layout);
  // both threads ended and the device closed; again is nothing
  void stop();

  void setMuted(bool muted);
  // packets go out for session (non-zero), the frames kept since the last detection first when preroll
  // (with prerollFromMs, a custom wake's instant in unix ms, the kept frames read from then on instead)
  void open(std::uint64_t session, bool preroll, std::int64_t prerollFromMs = 0);
  // and re-arms the spotter
  void close();
  // the custom wake word listens: frames with no session open go out as pcm events too
  void setListen(bool listen) { listen_.store(listen); }

  // once stopped
  [[nodiscard]] KeywordStats stats() const;

 private:
  struct Frame {
    std::array<std::int16_t, kEncoderFrameSamples> pcm{};
    std::int64_t atMs = 0;
    std::uint64_t seq = 0;
    // the session open when it was read, 0 for none
    std::uint64_t session = 0;
    // the spotter re-arms before it: audio was lost or silenced in between
    bool gap = false;
  };

  class Metered;

  // the reader thread's
  void read(const std::stop_token& stop);
  void heard(const std::array<std::int16_t, kEncoderFrameSamples>& pcm, std::int64_t atMs, bool gap);
  void encode(std::uint64_t session, std::span<const std::int16_t> pcm);
  void queue(const Frame& frame);
  // the spotter thread's
  void listen(const std::stop_token& stop);
  // after each frame: false once the model kept failing
  bool calls();

  std::unique_ptr<PcmSource> source_;
  KeywordEvents events_;
  const Log& log_;
  std::size_t queueFrames_;
  std::size_t arenaBytes_;
  // the spotter owns it; its counts are read on the spotter thread, and once stopped
  Metered* metered_ = nullptr;
  kws::KwsSpotter spotter_;
  CaptureLayout layout_{};
  std::optional<MicDownmix> downmix_;

  std::atomic<std::uint64_t> session_{0};
  std::atomic<std::uint64_t> prerollFor_{0};
  // a close: the reader forgets the kept frames before its next one
  std::atomic<bool> forget_{false};
  // a close: the spotter re-arms before its next frame
  std::atomic<bool> rearm_{false};
  // odd while muted; the loop alone moves it
  std::atomic<std::uint64_t> muteEpoch_{0};
  // the frame that completed the last detection
  std::atomic<std::uint64_t> wakeSeq_{0};
  std::atomic<bool> listen_{false};
  std::atomic<std::int64_t> prerollFrom_{0};
  // the reader's: the next listened frame follows lost or session audio
  bool listenGap_ = true;

  // the reader's: the kept frames, the seq of the last frame read, and the open session's encoder
  std::deque<Frame> preroll_;
  std::uint64_t seq_ = 0;
  std::uint64_t encoding_ = 0;
  std::unique_ptr<OpusEncoder> encoder_;
  std::uint64_t encodeStreak_ = 0;
  bool encodeFailed_ = false;
  std::uint64_t burstDrops_ = 0;
  std::uint64_t recoveries_ = 0;
  std::uint64_t readErrors_ = 0;

  // the queue to the spotter: a ring of kKeywordQueueFrames, closed once the spotter stopped
  std::mutex queueMutex_;
  std::condition_variable_any queued_;
  std::vector<Frame> ring_;
  std::size_t head_ = 0;
  std::size_t size_ = 0;
  bool closed_ = false;
  std::uint64_t dropped_ = 0;

  // the spotter's
  std::uint64_t wakes_ = 0;
  bool started_ = false;

  std::jthread listener_;
  std::jthread reader_;
  bool opened_ = false;
  bool stopped_ = false;
};

}  // namespace cadence::audiod
