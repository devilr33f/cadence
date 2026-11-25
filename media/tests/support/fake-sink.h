#pragma once

#include "playback/alsa-playback.h"

#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <mutex>
#include <optional>
#include <span>
#include <string>
#include <vector>

namespace cadence::fake_sink {

// scripted playback device that mixes like pickle's dmix. writes fill the device up to the requested fill, or
// the granted buffer when no fill is asked for; nothing is heard until the test plays it, so a test sees
// exactly what reached the speaker. a drop or a close leaves written frames playing, because dmix has already
// mixed them; only a rewind takes frames back, and never the period the hardware is playing
class FakePcmSink final : public PcmSink {
 public:
  // set before the player starts; by default open grants exactly what is requested
  std::optional<PlaybackFormat> grant;
  std::optional<PlaybackError> openError;

  // read after start or stop returns
  std::optional<PlaybackFormat> requested;
  int opens = 0;
  int closes = 0;

  // the device plays up to this many frames from its buffer and returns the interleaved samples heard
  std::vector<std::int16_t> play(std::size_t frames);
  // the next write finds the device ran dry: the stream restarts and nothing is written
  void pushUnderrun();
  // the next write fails and cannot be recovered
  void pushFailure();
  // delayFrames reports this error until cleared with std::nullopt, as a device whose delay is unreadable
  void failDelay(std::optional<PlaybackError> error = PlaybackError{PlaybackErrorCode::DelayFailed, "fake delay failure"});
  // delayFrames reports this instead of the buffered frames until cleared, so a test can hold the played
  // watermark still or push it past the submitted frames
  void setDelay(std::optional<std::size_t> frames);
  // holds the next write inside the sink until releaseWrites, so a test can land a cancel mid-write
  void blockWrites();
  void releaseWrites();
  [[nodiscard]] bool writeBlocked() const;

  Result<std::size_t, PlaybackError> delayFrames() override;

  // interleaved frames waiting in the device buffer
  [[nodiscard]] std::vector<std::int16_t> buffered() const;
  [[nodiscard]] std::size_t bufferedFrames() const;
  // the most frames the device ever held at once
  [[nodiscard]] std::size_t peakFrames() const;
  // every sample ever written, in order
  [[nodiscard]] std::vector<std::int16_t> written() const;
  // stream calls in order: r for rewind, d for drop, c for close
  [[nodiscard]] std::string calls() const;
  [[nodiscard]] std::size_t rewoundFrames() const;

  Result<PlaybackFormat, PlaybackError> open(const PlaybackFormat& format) override;
  PlaybackWrite write(std::span<const std::int16_t> interleaved, std::chrono::milliseconds timeout) override;
  Result<std::size_t, PlaybackError> rewind() override;
  std::optional<PlaybackError> drop() override;
  void close() override;

 private:
  mutable std::mutex mutex_;
  std::condition_variable room_;
  std::deque<std::int16_t> buffer_;
  std::vector<std::int16_t> written_;
  std::deque<PlaybackWrite::Kind> faults_;
  std::size_t channels_ = 2;
  std::size_t periodFrames_ = 0;
  std::size_t capacityFrames_ = 0;
  std::size_t peakFrames_ = 0;
  std::size_t rewoundFrames_ = 0;
  std::string calls_;
  std::optional<PlaybackError> delayError_;
  std::optional<std::size_t> delayOverride_;
  bool blockWrites_ = false;
  bool writeWaiting_ = false;
};

// left or right channel of interleaved stereo
std::vector<std::int16_t> channel(const std::vector<std::int16_t>& interleaved, std::size_t index);

}  // namespace cadence::fake_sink
