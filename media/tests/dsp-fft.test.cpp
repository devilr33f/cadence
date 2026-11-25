#include "dsp/fft.h"

#include <catch2/catch_template_test_macros.hpp>
#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <array>
#include <cmath>
#include <complex>
#include <cstddef>
#include <numbers>
#include <random>
#include <vector>

using namespace cadence::dsp;

namespace {

template <std::size_t N>
std::array<std::complex<float>, N> randomBlock(unsigned seed) {
  std::mt19937 random(seed);
  std::uniform_real_distribution<float> uniform(-1, 1);
  std::array<std::complex<float>, N> block;
  for (auto& value : block)
    value = {uniform(random), uniform(random)};
  return block;
}

template <std::size_t N>
std::vector<std::complex<double>> naiveDft(const std::array<std::complex<float>, N>& x) {
  std::vector<std::complex<double>> roots(N);
  for (std::size_t m = 0; m < N; ++m)
    roots[m] = std::polar(1.0, -2 * std::numbers::pi * static_cast<double>(m) / static_cast<double>(N));
  std::vector<std::complex<double>> spectrum(N);
  for (std::size_t k = 0; k < N; ++k)
    for (std::size_t n = 0; n < N; ++n)
      spectrum[k] += std::complex<double>(x[n]) * roots[(k * n) % N];
  return spectrum;
}

}  // namespace

TEMPLATE_TEST_CASE("the fft matches a naive dft", "", Fft1024, Fft2048) {
  constexpr std::size_t kN = TestType::kSize;
  const TestType fft;
  const auto input = randomBlock<kN>(11);
  const auto expected = naiveDft<kN>(input);
  auto data = input;
  fft.forward(data);

  double error = 0;
  for (std::size_t k = 0; k < kN; ++k)
    error = std::max(error, std::abs(std::complex<double>(data[k]) - expected[k]));
  CHECK(error < 1e-3);
}

TEMPLATE_TEST_CASE("parseval holds", "", Fft1024, Fft2048) {
  constexpr std::size_t kN = TestType::kSize;
  const TestType fft;
  auto data = randomBlock<kN>(12);
  double timeEnergy = 0;
  for (const auto& value : data)
    timeEnergy += std::norm(std::complex<double>(value));
  fft.forward(data);
  double frequencyEnergy = 0;
  for (const auto& value : data)
    frequencyEnergy += std::norm(std::complex<double>(value));
  CHECK(std::abs(frequencyEnergy / static_cast<double>(kN) - timeEnergy) / timeEnergy < 1e-5);
}
