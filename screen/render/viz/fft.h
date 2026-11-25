#pragma once

#include <array>
#include <cmath>
#include <complex>
#include <cstddef>
#include <numbers>

namespace cadence::render::viz {

// in-place iterative radix-2 forward fft of N points (a power of two), unnormalized, in double so a frame's bars come
// out alike on every cpu (the golden images)
template <std::size_t N>
class Fft {
  static_assert(N >= 2 && (N & (N - 1)) == 0);

 public:
  Fft() {
    for (std::size_t k = 0; k < N / 2; ++k) {
      const double a = -2.0 * std::numbers::pi * static_cast<double>(k) / static_cast<double>(N);
      twiddles_[k] = {std::cos(a), std::sin(a)};
    }
    std::size_t bits = 0;
    while ((std::size_t{1} << bits) < N) ++bits;
    for (std::size_t i = 0; i < N; ++i) {
      std::size_t r = 0;
      for (std::size_t b = 0; b < bits; ++b) r |= ((i >> b) & 1U) << (bits - 1 - b);
      reversed_[i] = r;
    }
  }

  void forward(std::array<std::complex<double>, N>& x) const {
    for (std::size_t i = 0; i < N; ++i) {
      if (i < reversed_[i]) std::swap(x[i], x[reversed_[i]]);
    }
    for (std::size_t len = 2; len <= N; len <<= 1) {
      const std::size_t step = N / len;
      for (std::size_t i = 0; i < N; i += len) {
        for (std::size_t j = 0; j < len / 2; ++j) {
          const std::complex<double> t = twiddles_[j * step] * x[i + j + len / 2];
          x[i + j + len / 2] = x[i + j] - t;
          x[i + j] += t;
        }
      }
    }
  }

 private:
  std::array<std::complex<double>, N / 2> twiddles_{};
  std::array<std::size_t, N> reversed_{};
};

}  // namespace cadence::render::viz
