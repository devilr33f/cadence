#include "sync/calibration/fft.h"

#include <algorithm>
#include <cmath>
#include <numbers>

namespace cadence::sync::calibration {

void Fft::operator()(std::vector<std::complex<float>>& data, bool inverse) {
  const auto n = data.size();
  if (n < 2 || (n & (n - 1)) != 0)
    return;
  auto [it, inserted] = tables_.try_emplace(n);
  auto& table = it->second;
  if (inserted) {
    unsigned bits = 0;
    while ((std::size_t{1} << bits) < n)
      ++bits;
    table.reverse.resize(n);
    for (std::size_t i = 0; i < n; ++i) {
      std::uint32_t x = static_cast<std::uint32_t>(i);
      std::uint32_t reversed = 0;
      for (unsigned b = 0; b < bits; ++b) {
        reversed = (reversed << 1) | (x & 1);
        x >>= 1;
      }
      table.reverse[i] = reversed;
    }
    table.roots.resize(n / 2);
    for (std::size_t k = 0; k < n / 2; ++k) {
      const double angle = -2.0 * std::numbers::pi * static_cast<double>(k) / static_cast<double>(n);
      table.roots[k] = {static_cast<float>(std::cos(angle)), static_cast<float>(std::sin(angle))};
    }
  }
  for (std::size_t i = 0; i < n; ++i)
    if (i < table.reverse[i])
      std::swap(data[i], data[table.reverse[i]]);
  for (std::size_t length = 2; length <= n; length <<= 1) {
    const auto half = length / 2;
    const auto stride = n / length;
    for (std::size_t first = 0; first < n; first += length)
      for (std::size_t k = 0; k < half; ++k) {
        auto w = table.roots[k * stride];
        if (inverse)
          w = std::conj(w);
        const auto even = data[first + k];
        const auto odd = data[first + k + half] * w;
        data[first + k] = even + odd;
        data[first + k + half] = even - odd;
      }
  }
}

}  // namespace cadence::sync::calibration
