#include "kws/spotter.h"

#include <algorithm>
#include <utility>

namespace cadence::kws {

SpotterPolicy SpotterPolicy::defaultCapsula() {
  return {.posThreshold = KWS_POS_THRESHOLD,
          .spThreshold = KWS_SP_THRESHOLD,
          .consecutiveCalls = KWS_CONSECUTIVE_CALLS,
          .rearmFrames = KWS_REARM_FRAMES};
}

KwsSpotter::KwsSpotter(std::unique_ptr<KwsModel> model, KwsFrontend frontend, SpotterPolicy policy)
    : model_(std::move(model)), frontend_(std::move(frontend)), policy_(policy) {
  frontend_.reset();
}

std::optional<Detection> KwsSpotter::push(std::span<const std::int16_t> pcm20ms, std::int64_t atMs) {
  for (const auto& frame : frontend_.push(pcm20ms)) {
    std::copy(frame.begin(), frame.end(), queued_.data() + queuedFrames_ * KwsModel::kMels);
    if (++queuedFrames_ < KwsModel::kFrames)
      continue;
    queuedFrames_ = 0;
    const auto score = call();
    if (!score) {
      firingCalls_ = 0;
      continue;
    }
    if (++firingCalls_ >= policy_.consecutiveCalls) {
      rearm();
      return Detection{.score = *score, .atMs = atMs};
    }
  }
  return std::nullopt;
}

void KwsSpotter::reset() { rearm(); }

void KwsSpotter::rearm() {
  frontend_.reset();
  queuedFrames_ = 0;
  heardFrames_ = 0;
  firingCalls_ = 0;
}

// the call's score when it fires
std::optional<float> KwsSpotter::call() {
  const auto out = model_->invoke(queued_);
  // as the stock: frames are counted until a call ends past the warm-up, which scores only its frames past it
  std::size_t scored = KwsModel::kFrames;
  if (heardFrames_ < policy_.rearmFrames) {
    heardFrames_ += KwsModel::kFrames;
    scored = heardFrames_ > policy_.rearmFrames ? heardFrames_ - policy_.rearmFrames : 0;
  }
  if (!out.ok() || scored == 0)
    return std::nullopt;

  const Outputs& outputs = out.value();
  std::size_t peak = KwsModel::kFrames - scored;
  for (std::size_t t = peak + 1; t < KwsModel::kFrames; ++t)
    if (outputs.posNeg[2 * t + 1] > outputs.posNeg[2 * peak + 1])
      peak = t;
  const float score = outputs.posNeg[2 * peak + 1];
  if (score >= policy_.posThreshold && outputs.intUnint[2 * peak + 1] >= policy_.spThreshold)
    return score;
  return std::nullopt;
}

}  // namespace cadence::kws
