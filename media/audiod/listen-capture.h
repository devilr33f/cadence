#pragma once

#include "audio-layout.h"
#include "audio/opus-codec.h"
#include "audiod/log.h"
#include "audiod/mic-layout.h"
#include "capture/alsa-capture.h"

#include <array>
#include <atomic>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <optional>
#include <span>
#include <stop_token>
#include <string_view>
#include <thread>
#include <vector>

namespace cadence::audiod {

// the 20 ms frames a listening capture keeps for a session's pre-roll: 500 ms, more than a custom wake takes to reach
// cadenced and come back as a CaptureStart
inline constexpr std::size_t kListenPrerollFrames = 25;

// a listening capture's events, from its reader thread: each only posts
struct ListenEvents {
  // 20 ms of 16 kHz mono while no session is open (the custom wake word's input): seq counts the frames read, at_ms is
  // when it was read (unix ms), gap that audio was lost or silenced since the last one
  std::function<void(std::uint64_t seq, std::int64_t atMs, std::vector<std::int16_t> pcm, bool gap)> pcm;
  // one 20 ms packet of the open session
  std::function<void(std::uint64_t session, std::vector<std::uint8_t> opus)> packet;
  // the encoder kept failing: nothing more goes out for this session
  std::function<void(std::uint64_t session)> failed;
};

// what a listening capture counted, read once it stopped
struct ListenStats {
  std::uint64_t frames = 0;
  std::uint64_t recoveries = 0;
  std::uint64_t readErrors = 0;
};

// the microphone held open for the custom wake word on a board without engines of its own (raw capture): a reader
// thread takes the device's frames, the layout's microphones mixed to 16 kHz mono, 20 ms at a time. with no session
// open each frame goes out as pcm and the last kListenPrerollFrames are kept; an open session's frames are encoded into
// 24 kbps voip packets instead, the kept ones read after its pre-roll instant first. the mute switch lets nothing out
// and nothing be kept, and the device's buffer that follows an unmute is silenced, as the keyword capture does.
// start, stop and the rest are the loop thread's
class ListenCapture {
 public:
  ListenCapture(std::unique_ptr<PcmSource> source, ListenEvents events, const Log& log);
  ~ListenCapture();

  ListenCapture(const ListenCapture&) = delete;
  ListenCapture& operator=(const ListenCapture&) = delete;

  // opens the device in the layout's format and starts the reader. the error is a token: format, open or thread
  std::optional<std::string_view> start(const CaptureLayout& layout);
  // the reader ended and the device closed; again is nothing
  void stop();

  void setMuted(bool muted);
  // packets go out for session (non-zero), first the kept frames read after prerollFromMs (unix ms) when it is not 0
  void open(std::uint64_t session, std::int64_t prerollFromMs);
  void close();

  [[nodiscard]] ListenStats stats() const;

 private:
  struct Frame {
    std::array<std::int16_t, kEncoderFrameSamples> pcm{};
    std::int64_t atMs = 0;
  };

  void read(const std::stop_token& stop);
  void heard(const std::array<std::int16_t, kEncoderFrameSamples>& pcm, std::int64_t atMs, bool gap);
  void encode(std::uint64_t session, std::span<const std::int16_t> pcm);

  std::unique_ptr<PcmSource> source_;
  ListenEvents events_;
  const Log& log_;
  CaptureLayout layout_{};
  std::optional<MicDownmix> downmix_;

  std::atomic<std::uint64_t> session_{0};
  std::atomic<std::int64_t> prerollFrom_{0};
  // a close: the reader forgets the kept frames, and the next pcm frame is a gap
  std::atomic<bool> closed_{false};
  // odd while muted; the loop alone moves it
  std::atomic<std::uint64_t> muteEpoch_{0};

  // the reader's
  std::deque<Frame> preroll_;
  std::uint64_t seq_ = 0;
  std::uint64_t encoding_ = 0;
  std::unique_ptr<OpusEncoder> encoder_;
  std::uint64_t encodeStreak_ = 0;
  bool encodeFailed_ = false;
  std::uint64_t recoveries_ = 0;
  std::uint64_t readErrors_ = 0;

  std::jthread reader_;
  bool opened_ = false;
  bool stopped_ = false;
};

}  // namespace cadence::audiod
