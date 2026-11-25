#pragma once

#include "result.h"

#include <array>
#include <cstddef>
#include <span>
#include <string_view>

namespace cadence::kws {

// one model call's outputs, frame-major as the model lays them out: for frame t (0 oldest), posNeg[2t + 1] is the
// keyword's probability, intUnint[2t + 1] the probability it was meant, and kwLoc[t] where the keyword ends in the
// model's context (1 the newest frame)
struct Outputs {
  std::array<float, 16> posNeg{};
  std::array<float, 8> kwLoc{};
  std::array<float, 16> intUnint{};
};

// what the spotter runs its calls through: the tflm runner (tflm-model.h), or a scripted fake in tests
class KwsModel {
 public:
  static constexpr std::size_t kFrames = 8;
  static constexpr std::size_t kMels = 40;

  virtual ~KwsModel() = default;

  // one call: 8 frames of 40 features, oldest first, mel index fastest. the error is a token, safe to log
  virtual Result<Outputs, std::string_view> invoke(std::span<const float, kFrames * kMels> features) = 0;
};

}  // namespace cadence::kws
