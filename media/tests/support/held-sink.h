#pragma once

#include "audiod/output.h"
#include "support/fake-sink.h"

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <optional>
#include <span>
#include <string_view>

namespace cadence::test_support {

// the player's device is the test's fake, played by hand
class HeldSink final : public PcmSink {
 public:
  explicit HeldSink(fake_sink::FakePcmSink& sink) : sink_(sink) {}
  Result<PlaybackFormat, PlaybackError> open(const PlaybackFormat& requested) override { return sink_.open(requested); }
  PlaybackWrite write(std::span<const std::int16_t> interleaved, std::chrono::milliseconds timeout) override {
    return sink_.write(interleaved, timeout);
  }
  Result<std::size_t, PlaybackError> rewind() override { return sink_.rewind(); }
  Result<std::size_t, PlaybackError> delayFrames() override { return sink_.delayFrames(); }
  std::optional<PlaybackError> drop() override { return sink_.drop(); }
  void close() override { sink_.close(); }

 private:
  fake_sink::FakePcmSink& sink_;
};

// every output the harness opens plays into sink, with no hardware control
inline audiod::OutputFactory heldOutput(fake_sink::FakePcmSink& sink) {
  return [&sink](const audiod::OutputConfig&) -> Result<audiod::Output, std::string_view> {
    audiod::Output output;
    output.sink = std::make_unique<HeldSink>(sink);
    return output;
  };
}

}  // namespace cadence::test_support
