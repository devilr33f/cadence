#pragma once

#include "aaudio/frame-ring.h"
#include "aaudio/stream.h"
#include "capture/alsa-capture.h"

#include <atomic>
#include <cstddef>
#include <cstdint>
#include <memory>

namespace cadence {

// audiod's capture device on android (a usb microphone: the stick's remote delivers nothing to an app, S4b): an aaudio
// input stream, s16 at the requested rate and channels exactly, whose data callback fills a ring the engine reads.
// the ring holds at least four bursts and 200 ms; a callback that finds it full drops what does not fit, and the next
// read drops what is older and reports Recovered (audio was lost), as alsa's overrun. a stream that dies is opened again by the next read,
// which reports Recovered
class AAudioCapture final : public PcmSource {
 public:
  explicit AAudioCapture(aaudio::Api& api);
  ~AAudioCapture() override;

  AAudioCapture(const AAudioCapture&) = delete;
  AAudioCapture& operator=(const AAudioCapture&) = delete;

  // grants the requested period (any read size works) and the ring as the buffer
  Result<CaptureFormat, CaptureError> open(const CaptureFormat& requested) override;
  CaptureRead read(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) override;
  void close() override;

  [[nodiscard]] std::uint64_t overruns() const { return overruns_.load(); }

 private:
  std::optional<CaptureError> openStream();
  void onData(std::span<std::int16_t> in);

  aaudio::Api& api_;
  CaptureFormat format_;
  std::unique_ptr<aaudio::Stream> stream_;
  aaudio::FrameRing ring_;
  bool open_ = false;

  std::atomic<bool> lost_{false};
  std::atomic<bool> dead_{false};
  std::atomic<std::uint64_t> overruns_{0};
};

}  // namespace cadence
