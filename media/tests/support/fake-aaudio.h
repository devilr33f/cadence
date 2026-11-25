#pragma once

#include "aaudio/stream.h"

#include <cstdint>
#include <memory>
#include <optional>
#include <span>
#include <string>
#include <vector>

namespace cadence::fake_aaudio {

// a scripted aaudio: a stream's callbacks run only when the test pumps it, on the test's thread, and its clock and
// timestamps are the test's
class FakeApi final : public aaudio::Api {
 public:
  struct FakeStream;

  // set before open; by default a stream gets what was asked for
  std::optional<int> grantRate;
  std::optional<int> grantChannels;
  int burst = 192;
  std::optional<std::string> openError;
  bool startFails = false;

  std::int64_t now = 1'000'000'000;
  std::vector<aaudio::StreamRequest> requests;
  int opens = 0;

  Result<std::unique_ptr<aaudio::Stream>, std::string> open(const aaudio::StreamRequest& request,
                                                            aaudio::DataCallback data,
                                                            aaudio::ErrorCallback error) override;
  [[nodiscard]] std::int64_t nowNs() const override { return now; }

  // the live stream, null before an open or once closed
  [[nodiscard]] FakeStream* stream() const { return live_; }

  // an output callback of frames: what aaudio would play, interleaved
  std::vector<std::int16_t> pull(std::size_t frames);
  // an input callback carrying these interleaved samples
  void push(std::span<const std::int16_t> interleaved);
  // the stream dies, as on a disconnect
  void disconnect();

  struct FakeStream final : public aaudio::Stream {
    FakeApi* api = nullptr;
    aaudio::StreamRequest request;
    aaudio::StreamInfo granted;
    aaudio::DataCallback data;
    aaudio::ErrorCallback error;
    bool started = false;
    bool stopped = false;
    std::int64_t written = 0;
    std::int64_t read = 0;
    std::optional<aaudio::Timestamp> ts;
    std::int32_t xrunCount = 0;

    ~FakeStream() override;
    [[nodiscard]] aaudio::StreamInfo info() const override { return granted; }
    bool start() override;
    void stop() override { stopped = true; }
    [[nodiscard]] std::int64_t framesWritten() const override { return written; }
    [[nodiscard]] std::int64_t framesRead() const override { return read; }
    [[nodiscard]] std::optional<aaudio::Timestamp> timestamp() const override { return ts; }
    [[nodiscard]] std::int32_t xruns() const override { return xrunCount; }
  };

 private:
  FakeStream* live_ = nullptr;
};

}  // namespace cadence::fake_aaudio
