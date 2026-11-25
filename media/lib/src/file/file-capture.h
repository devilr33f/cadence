#pragma once

#include "capture/alsa-capture.h"
#include "file/file-clock.h"
#include "file/wav.h"

#include <cstddef>
#include <cstdint>
#include <string>

namespace cadence {

// plays a 16 kHz mono wav as a microphone: each open starts it over, and audio arrives at real-time pace in
// 20 ms frames (the last one may be shorter). once the file is played, reads find nothing until they time out
class FileCapture final : public PcmSource {
 public:
  explicit FileCapture(std::string path, FileClock& clock = realFileClock());

  // reports the file's own format; a file that is not 16 kHz mono fails with FormatMismatch
  Result<CaptureFormat, CaptureError> open(const CaptureFormat& requested) override;
  CaptureRead read(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) override;
  void close() override;

  // every sample has been handed out
  [[nodiscard]] bool finished() const;

 private:
  std::string path_;
  FileClock& clock_;
  bool open_ = false;
  WavReader reader_;
  FileClock::TimePoint start_{};
  std::size_t position_ = 0;
};

}  // namespace cadence
