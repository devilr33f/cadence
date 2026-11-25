#pragma once

#include "capture/alsa-capture.h"

#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <mutex>
#include <optional>
#include <span>
#include <vector>

namespace cadence::fake_pcm {

// scripted capture source: open grants what the test chooses, reads replay queued audio and faults
class FakePcmSource final : public PcmSource {
 public:
  // set before the engine starts; by default open grants exactly what is requested
  std::optional<CaptureFormat> grant;
  std::optional<CaptureError> openError;

  // read after start or stop returns
  std::optional<CaptureFormat> requested;
  int opens = 0;
  int closes = 0;

  // reads hand queued frames out at most one read buffer at a time
  void pushFrames(std::vector<std::int16_t> interleaved);
  // an overrun the source recovered from: audio was lost
  void pushRecovered();
  // a read that failed and could not be recovered
  void pushFailure();
  // while held, reads find nothing, as if audio piled up in the device buffer between reads
  void holdReads(bool held);
  // every queued step has been read
  [[nodiscard]] bool drained() const;
  // blocks until the first read has begun, found audio or not; false on timeout
  [[nodiscard]] bool waitForFirstRead(std::chrono::milliseconds limit = std::chrono::seconds(5));
  // the reads begun so far: one begun after the queue drained means the reader handled everything before it
  [[nodiscard]] std::size_t reads() const;

  Result<CaptureFormat, CaptureError> open(const CaptureFormat& format) override;
  CaptureRead read(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) override;
  void close() override;

 private:
  struct Step {
    CaptureRead::Kind kind = CaptureRead::Kind::Frames;
    std::vector<std::int16_t> samples;
    std::size_t offset = 0;
  };

  void push(Step step);

  mutable std::mutex mutex_;
  std::condition_variable queued_;
  std::condition_variable firstRead_;
  std::deque<Step> steps_;
  std::size_t channels_ = 4;
  bool held_ = false;
  bool read_ = false;
  std::size_t reads_ = 0;
};

// interleaved capture frames with sample(frame, channel) in each slot
template <typename Sample>
std::vector<std::int16_t> captureFrames(std::size_t count, std::size_t channels, Sample sample) {
  std::vector<std::int16_t> samples;
  samples.reserve(count * channels);
  for (std::size_t frame = 0; frame < count; ++frame)
    for (std::size_t channel = 0; channel < channels; ++channel)
      samples.push_back(sample(frame, channel));
  return samples;
}

}  // namespace cadence::fake_pcm
