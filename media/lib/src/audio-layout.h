#pragma once

#include <cstddef>
#include <string>
#include <vector>

namespace cadence {

// capture channel indices are positions within one interleaved frame. the period is required exactly
struct CaptureLayout {
  std::string device;
  int sampleRate;
  std::size_t channels;
  std::vector<std::size_t> microphones;
  std::vector<std::size_t> speakers;
  std::size_t periodFrames;
  std::size_t bufferFrames;
};

}  // namespace cadence
