#pragma once

#include <complex>
#include <cstddef>
#include <cstdint>
#include <map>
#include <vector>

namespace cadence::sync::calibration {

// iterative radix-2 transforms; each instance caches a size's reversal and forward roots, shared by both directions
class Fft {
 public:
  // data's size must be a power of two; the inverse is unnormalized
  void operator()(std::vector<std::complex<float>>& data, bool inverse);

 private:
  struct Table {
    std::vector<std::uint32_t> reverse;
    std::vector<std::complex<float>> roots;
  };
  std::map<std::size_t, Table> tables_;
};

}  // namespace cadence::sync::calibration
