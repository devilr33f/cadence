#pragma once

#include "result.h"

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <optional>
#include <span>
#include <string>

namespace cadence {

enum class SampleFormat {
  S16Le,
  // anything else the device might report
  Unsupported,
};

// interleaved playback parameters
struct PlaybackFormat {
  int sampleRate = 0;
  std::size_t channels = 0;
  SampleFormat sampleFormat = SampleFormat::S16Le;
  std::size_t periodFrames = 0;
  std::size_t bufferFrames = 0;
  // the most frames the player keeps in the device: writes stop at this fill and the stream starts once it
  // is reached. the player asks for it; a sink grants it back unchanged
  std::size_t fillFrames = 0;
};

enum class PlaybackErrorCode {
  // from AlsaPlayer::start
  AlreadyRunning,
  OpenFailed,
  ConfigureFailed,
  FormatMismatch,
  NotOpen,
  Underrun,
  Suspended,
  WriteFailed,
  DiscardFailed,
  // the device could not report how many written frames are still unplayed
  DelayFailed,
};

// detail names the device or alsa step and its error text; never audio
struct PlaybackError {
  PlaybackErrorCode code;
  std::string detail;
};

// bounded single-line text, safe to log
std::string describe(const PlaybackError& error);

// playback is never resampled or remixed on the way out, so rate, channels, and sample format must match
// exactly; period and buffer are the device's choice as long as a period fits in the buffer and the requested
// fill lies between two periods and the buffer
std::optional<PlaybackError> checkPlaybackFormat(const PlaybackFormat& requested, const PlaybackFormat& granted);

struct PlaybackWrite {
  enum class Kind {
    // no room within the timeout
    Idle,
    Frames,
    // the device ran dry or was suspended and the stream was restarted; nothing was written
    Recovered,
    // the stream failed and could not be restarted
    Failed,
  };
  Kind kind = Kind::Idle;
  std::size_t frames = 0;
  // set for Recovered and Failed
  std::optional<PlaybackError> error;
};

// the player's view of a playback device; used from one thread at a time
class PcmSink {
 public:
  virtual ~PcmSink() = default;
  // reports what the device granted
  virtual Result<PlaybackFormat, PlaybackError> open(const PlaybackFormat& requested) = 0;
  // writes at most interleaved.size() / channels frames and never fills the device past the granted fill,
  // waiting up to timeout for room
  virtual PlaybackWrite write(std::span<const std::int16_t> interleaved, std::chrono::milliseconds timeout) = 0;
  // takes back every frame written but not yet played, as far as the device allows, and reports how many.
  // on dmix this is the only way to unmix audio: a drop leaves what was already mixed playing
  virtual Result<std::size_t, PlaybackError> rewind() = 0;
  // frames written but not yet played, from the device's own delay reporting; a device that reports a
  // negative delay (a stream that ran dry) reads as zero. the played watermark is never derived from frame
  // counts alone, so an unavailable measurement is an error, not a guess
  virtual Result<std::size_t, PlaybackError> delayFrames() = 0;
  // stops the stream, leaving it ready for writes
  virtual std::optional<PlaybackError> drop() = 0;
  virtual void close() = 0;
};

// nonblocking alsa playback on the board's device. an underrun stops the stream instead of replaying the device
// buffer, and the stream restarts after underruns and suspends
class AlsaPlayback final : public PcmSink {
 public:
  explicit AlsaPlayback(std::string device);
  ~AlsaPlayback() override;

  AlsaPlayback(const AlsaPlayback&) = delete;
  AlsaPlayback& operator=(const AlsaPlayback&) = delete;

  Result<PlaybackFormat, PlaybackError> open(const PlaybackFormat& requested) override;
  PlaybackWrite write(std::span<const std::int16_t> interleaved, std::chrono::milliseconds timeout) override;
  Result<std::size_t, PlaybackError> rewind() override;
  Result<std::size_t, PlaybackError> delayFrames() override;
  std::optional<PlaybackError> drop() override;
  void close() override;

 private:
  struct Pcm;

  std::string device_;
  std::unique_ptr<Pcm> pcm_;
  std::size_t channels_ = 0;
  // the part of the buffer above the fill cap, which is never written
  std::size_t reservedFrames_ = 0;
};

}  // namespace cadence
