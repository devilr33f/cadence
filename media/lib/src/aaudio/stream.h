#pragma once

#include "result.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <span>
#include <string>

// the seam between audiod's aaudio backends and android's AAudio: the ndk's api on the device (ndk-api.cpp), a fake
// in the host tests. a stream runs a data callback on aaudio's own high-priority thread, which never blocks, locks or
// allocates
namespace cadence::aaudio {

enum class Direction { Output, Input };

struct StreamRequest {
  Direction direction = Direction::Output;
  int sampleRate = 48000;
  int channels = 2;
  // AAUDIO_PERFORMANCE_MODE_LOW_LATENCY: on the stick ~83 ms of output latency against ~300 ms in the default mode,
  // which also under-reports its delay (s5 device half)
  bool lowLatency = true;
};

// what the stream got
struct StreamInfo {
  int sampleRate = 0;
  int channels = 0;
  // frames aaudio moves per burst; a data callback asks for about this many (2,050 on the stick in s5)
  int framesPerBurst = 0;
  int bufferFrames = 0;
  bool lowLatency = false;
};

// a frame's presentation (output) or capture (input) time on CLOCK_MONOTONIC
struct Timestamp {
  std::int64_t framePosition = 0;
  std::int64_t monotonicNs = 0;
};

// fills (output) or takes (input) the interleaved s16 frames of one callback
using DataCallback = std::function<void(std::span<std::int16_t> interleaved)>;
// the stream died (a disconnect: the hdmi sink went, a route changed); on aaudio's thread, which must not close it
using ErrorCallback = std::function<void(int code)>;

class Stream {
 public:
  virtual ~Stream() = default;
  [[nodiscard]] virtual StreamInfo info() const = 0;
  virtual bool start() = 0;
  // no callback runs once it returns
  virtual void stop() = 0;
  // frames the callbacks have handed to (output) or taken from (input) aaudio
  [[nodiscard]] virtual std::int64_t framesWritten() const = 0;
  [[nodiscard]] virtual std::int64_t framesRead() const = 0;
  // none until the stream has run a while
  [[nodiscard]] virtual std::optional<Timestamp> timestamp() const = 0;
  [[nodiscard]] virtual std::int32_t xruns() const = 0;
};

class Api {
 public:
  virtual ~Api() = default;
  // the error is a short reason, safe to log; the callbacks live as long as the stream
  virtual Result<std::unique_ptr<Stream>, std::string> open(const StreamRequest& request, DataCallback data,
                                                            ErrorCallback error) = 0;
  // CLOCK_MONOTONIC in ns, the timestamps' clock
  [[nodiscard]] virtual std::int64_t nowNs() const = 0;
};

#ifdef __ANDROID__
// the ndk's AAudio (libaaudio.so, api 26+)
Api& ndkApi();
#endif

}  // namespace cadence::aaudio
