#include "dsp/fft.h"

#include <bit>
#include <cmath>
#include <numbers>
#include <utility>

namespace cadence::dsp {

template <std::size_t N>
Fft<N>::Fft() {
  constexpr unsigned kBits = static_cast<unsigned>(std::countr_zero(N));
  for (std::size_t k = 0; k < kSize / 2; ++k) {
    const double angle = -2 * std::numbers::pi * static_cast<double>(k) / static_cast<double>(kSize);
    twiddles_[k] = {static_cast<float>(std::cos(angle)), static_cast<float>(std::sin(angle))};
  }
  for (std::size_t i = 0; i < kSize; ++i) {
    unsigned reversed = 0;
    for (unsigned bit = 0; bit < kBits; ++bit)
      if ((i >> bit) & 1u)
        reversed |= 1u << (kBits - 1 - bit);
    reversed_[i] = static_cast<std::uint16_t>(reversed);
  }
}

template <std::size_t N>
void Fft<N>::forward(std::span<std::complex<float>, kSize> data) const {
  for (std::size_t i = 0; i < kSize; ++i)
    if (i < reversed_[i])
      std::swap(data[i], data[reversed_[i]]);

  for (std::size_t length = 2; length <= kSize; length <<= 1) {
    const std::size_t half = length / 2;
    const std::size_t stride = kSize / length;
    for (std::size_t start = 0; start < kSize; start += length) {
      for (std::size_t k = 0; k < half; ++k) {
        const std::complex<float> w = twiddles_[k * stride];
        std::complex<float>& a = data[start + k];
        std::complex<float>& b = data[start + k + half];
        // spelled out: std::complex's operator* checks for inf and nan on every product
        const std::complex<float> t(b.real() * w.real() - b.imag() * w.imag(),
                                    b.real() * w.imag() + b.imag() * w.real());
        b = a - t;
        a += t;
      }
    }
  }
}

template class Fft<1024>;
template class Fft<2048>;

}  // namespace cadence::dsp
