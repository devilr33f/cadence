#pragma once

#include <cstdint>
#include <span>

namespace cadence::dsp {

// xorshift64: fast, and plenty for dither noise
class Rng {
 public:
  explicit Rng(std::uint64_t seed);

  std::uint32_t next();

 private:
  std::uint64_t state_;
};

// float -1..1 to s16 with tpdf dither of +-1 lsb (two uniform randoms), rounded and clamped; nan is silence.
// converts min(in, out) samples
void ditherToS16(std::span<const float> in, std::span<std::int16_t> out, Rng& rng);

}  // namespace cadence::dsp
