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

// interleaved s16_le capture parameters
struct CaptureFormat {
  int sampleRate = 0;
  std::size_t channels = 0;
  std::size_t periodFrames = 0;
  std::size_t bufferFrames = 0;
};

enum class CaptureErrorCode {
  OpenFailed,
  ConfigureFailed,
  FormatMismatch,
  NotOpen,
  Overrun,
  Suspended,
  ReadFailed,
};

// detail names the device or alsa step and its error text; never audio
struct CaptureError {
  CaptureErrorCode code;
  std::string detail;
};

// bounded single-line text, safe to log
std::string describe(const CaptureError& error);

// the vqe preset is built for one rate and channel layout, so capture is never resampled or remixed, and the
// period must match too (longer periods made pickle reads fail with EIO); only the buffer size may differ
std::optional<CaptureError> checkExactFormat(const CaptureFormat& requested, const CaptureFormat& granted);

struct CaptureRead {
  enum class Kind {
    // nothing arrived within the timeout
    Idle,
    Frames,
    // audio was lost (overrun or suspend) and the stream was restarted
    Recovered,
    // the stream failed and could not be restarted
    Failed,
  };
  Kind kind = Kind::Idle;
  std::size_t frames = 0;
  // why audio was lost; set for Recovered and Failed
  std::optional<CaptureError> error;
};

// the audio engine's view of a capture device; used from one thread at a time
class PcmSource {
 public:
  virtual ~PcmSource() = default;
  // reports what the device granted
  virtual Result<CaptureFormat, CaptureError> open(const CaptureFormat& requested) = 0;
  // reads at most interleaved.size() / channels frames, waiting up to timeout for audio
  virtual CaptureRead read(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) = 0;
  virtual void close() = 0;
};

// nonblocking alsa capture that restarts the stream after overruns and suspends
class AlsaCapture final : public PcmSource {
 public:
  explicit AlsaCapture(std::string device);
  ~AlsaCapture() override;

  AlsaCapture(const AlsaCapture&) = delete;
  AlsaCapture& operator=(const AlsaCapture&) = delete;

  Result<CaptureFormat, CaptureError> open(const CaptureFormat& requested) override;
  CaptureRead read(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) override;
  void close() override;

 private:
  struct Pcm;

  std::string device_;
  std::unique_ptr<Pcm> pcm_;
  std::size_t channels_ = 0;
};

}  // namespace cadence
