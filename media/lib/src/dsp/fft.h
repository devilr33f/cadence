#pragma once

#include <array>
#include <complex>
#include <cstddef>
#include <cstdint>
#include <span>

namespace cadence::dsp {

// in-place iterative radix-2 forward fft of N points, unnormalized (X[k] = sum x[n] e^(-2 pi i k n / N)). fft.cpp
// instantiates the sizes below
template <std::size_t N>
class Fft {
  static_assert(N >= 2 && (N & (N - 1)) == 0 && N <= 65536, "a power of two whose indices fit in 16 bits");

 public:
  static constexpr std::size_t kSize = N;

  Fft();

  void forward(std::span<std::complex<float>, kSize> data) const;

 private:
  std::array<std::complex<float>, kSize / 2> twiddles_{};
  std::array<std::uint16_t, kSize> reversed_{};
};

extern template class Fft<1024>;
extern template class Fft<2048>;

using Fft1024 = Fft<1024>;
using Fft2048 = Fft<2048>;

}  // namespace cadence::dsp
