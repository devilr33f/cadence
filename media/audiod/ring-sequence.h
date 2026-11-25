#pragma once

#include "playback/mixer.h"

#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <span>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::audiod {

// a ring's url, decoded on a thread of its own into 48 kHz mono the playback thread reads. every call is safe from any
// thread and none blocks
class RingStream {
 public:
  struct Read {
    std::size_t samples = 0;
    // this pass's last sample was read
    bool ended = false;
  };

  virtual ~RingStream() = default;
  // up to out.size() samples decoded so far
  virtual Read read(std::span<std::int16_t> out) = 0;
  // the next reads play the url from its start again (the ring looped back to it)
  virtual void rewind() = 0;
  // the fixed code it failed with (a url code, or unsupported), empty while it has not
  [[nodiscard]] virtual std::string_view failure() const = 0;
  // its thread lets go of the url; reads after it give nothing
  virtual void stop() = 0;
};

struct RingStreamRequest {
  std::string url;
  // the Authorization header's value: never in the url, never logged
  std::string authorization;
  // Configure's ca bundle
  std::string caFile;
};

// starts decoding the url at once; null when this build cannot (a screen's, without ffmpeg)
using RingStreamFactory = std::function<std::shared_ptr<RingStream>(const RingStreamRequest& request)>;

// one stretch of a ring: samples played once (a tone's period, a chime, a prompt), silence, or a stream
struct RingPart {
  std::shared_ptr<const std::vector<std::int16_t>> pcm{};
  std::size_t silence = 0;
  std::shared_ptr<RingStream> stream{};
};

// how long a stream may give nothing, at 48 kHz, before the ring falls back: 3 s
inline constexpr std::size_t kRingStallSamples = 144000;

// a ring of segments, as the mixer loops it: the parts in order, from the first again after the last when it loops
// (else silence), and fallback (no stream in it, always looped) for the rest of the ring once a stream failed or gave
// nothing for kRingStallSamples. onFallback runs once, on the playback thread, with the stream's code (timeout for a
// stall), so it only posts. fill runs on the playback thread alone
class RingSequence final : public LoopFeed {
 public:
  using FallbackHook = std::function<void(std::string_view code)>;

  RingSequence(std::vector<RingPart> parts, bool loop, std::vector<RingPart> fallback, FallbackHook onFallback);
  ~RingSequence() override;
  RingSequence(const RingSequence&) = delete;
  RingSequence& operator=(const RingSequence&) = delete;

  void fill(std::span<std::int16_t> out) override;

 private:
  // the part after the one that ended; a stream entered again starts over
  void advance();
  void fallBack(std::string_view code);
  void stopStreams();

  std::vector<RingPart> parts_;
  bool loop_;
  std::vector<RingPart> fallback_;
  FallbackHook onFallback_;
  std::vector<bool> entered_;
  std::size_t index_ = 0;
  // samples of the part played so far
  std::size_t offset_ = 0;
  // samples a stream owed in a row
  std::size_t starved_ = 0;
  bool done_ = false;
  bool fellBack_ = false;
};

}  // namespace cadence::audiod
