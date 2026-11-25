#pragma once

// tflm's int8 requantization on four lanes, for the keyword runner's kernels on aarch64. tflm builds with double
// rounding (TFLITE_SINGLE_ROUNDING unset): MultiplyByQuantizedMultiplier is gemmlowp's
// RoundingDivideByPOT(SaturatingRoundingDoublingHighMul(x << left, multiplier), right). vqrdmulh is exactly
// SaturatingRoundingDoublingHighMul, and gemmlowp's own neon RoundingDivideByPOT (the and/shift fixup, a saturating add
// and a rounding shift) gives the scalar one's result, so these match the reference bit for bit

#include "tensorflow/lite/micro/kernels/internal/common.h"

#if defined(__aarch64__) && defined(__ARM_NEON)
#define CADENCE_KWS_NEON 1
#include <arm_neon.h>
#endif

#if TFLITE_SINGLE_ROUNDING
#error "the keyword kernels match tflm's double rounding; single rounding needs other lanes"
#endif

namespace cadence::kws {

#if defined(CADENCE_KWS_NEON)

// RoundingDivideByPOT with each lane's exponent given negated (0 or less)
inline int32x4_t roundingDivideByPot(int32x4_t x, int32x4_t negExponent) {
  const int32x4_t fixup = vshrq_n_s32(vandq_s32(x, negExponent), 31);
  return vrshlq_s32(vqaddq_s32(x, fixup), negExponent);
}

// MultiplyByQuantizedMultiplier with each lane's multiplier and shift (per channel)
inline int32x4_t multiplyByQuantizedMultiplier(int32x4_t x, int32x4_t multiplier, int32x4_t shift) {
  const int32x4_t zero = vdupq_n_s32(0);
  return roundingDivideByPot(vqrdmulhq_s32(vshlq_s32(x, vmaxq_s32(shift, zero)), multiplier), vminq_s32(shift, zero));
}

// MultiplyByQuantizedMultiplierSmallerThanOneExp: one multiplier, its shift 0 or less
inline int32x4_t multiplyByQuantizedMultiplierSmallerThanOneExp(int32x4_t x, int32_t multiplier, int32x4_t shift) {
  return roundingDivideByPot(vqrdmulhq_n_s32(x, multiplier), shift);
}

#endif

}  // namespace cadence::kws
