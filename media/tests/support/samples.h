#pragma once

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <iterator>
#include <numeric>
#include <vector>

namespace cadence::test_support {

// samples first, first + 1, ... so every sample can be traced to where it was pushed
inline std::vector<std::int16_t> ramp(std::size_t count, int first = 1) {
  std::vector<std::int16_t> samples(count);
  std::iota(samples.begin(), samples.end(), static_cast<std::int16_t>(first));
  return samples;
}

inline std::vector<std::int16_t> constant(std::size_t count, std::int16_t value) {
  return std::vector<std::int16_t>(count, value);
}

inline bool silent(const std::vector<std::int16_t>& samples) {
  return std::all_of(samples.begin(), samples.end(), [](std::int16_t sample) { return sample == 0; });
}

// the nonzero samples, in order
inline std::vector<std::int16_t> audible(const std::vector<std::int16_t>& samples) {
  std::vector<std::int16_t> heard;
  std::copy_if(samples.begin(), samples.end(), std::back_inserter(heard), [](std::int16_t sample) { return sample != 0; });
  return heard;
}

}  // namespace cadence::test_support
