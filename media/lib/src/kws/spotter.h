#pragma once

#include "kws/frontend.h"
#include "kws/model.h"

#include <array>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <optional>
#include <span>

namespace cadence::kws {

// the vk capsula mini's wake policy: the stock's, with the keyword threshold raised from its 0.994, at which the stock
// wakes on speech without the keyword (it leaves those to a cloud check of the keyword that cadence lacks)
inline constexpr float KWS_POS_THRESHOLD = 0.999f;
inline constexpr float KWS_SP_THRESHOLD = 0.2f;
inline constexpr unsigned KWS_CONSECUTIVE_CALLS = 1;
inline constexpr unsigned KWS_REARM_FRAMES = 147;

// how a call's outputs become a detection. a call fires when its score, the highest keyword probability
// (pos_neg[..., 1]) over the frames it scores, reaches posThreshold, and the probability the keyword was meant
// (int_unint[..., 1]) at the first of those frames with that score reaches spThreshold. nothing is smoothed or
// carried across calls but the count of calls in a row that fired
struct SpotterPolicy {
  float posThreshold = 0;
  float spThreshold = 0;
  // the calls in a row that must fire for a detection
  unsigned consecutiveCalls = 1;
  // the frames no call scores after the start, a reset and every detection. the model's own state is never reset,
  // so its context flushes in them
  unsigned rearmFrames = 0;

  // KWS_POS_THRESHOLD, KWS_SP_THRESHOLD, KWS_CONSECUTIVE_CALLS and KWS_REARM_FRAMES, whether or not media plays
  static SpotterPolicy defaultCapsula();
};

struct Detection {
  // the score of the call that completed the detection
  float score = 0;
  // the atMs of the push whose samples completed that call
  std::int64_t atMs = 0;
};

// the keyword spotter: the frontend's frames go to the model 8 at a time, oldest first and without overlap, and each
// call's outputs through the policy. the first rearmFrames frames after the start, a reset or a detection are not
// scored (a call that ends past them scores only its frames past them). a detection drops the rest of its push and
// re-arms: the frontend's samples, the queued frames and the counts start again, but the model's state carries on,
// as the stock's does through every wake. holding wakes off for a turn is the caller's. one thread at a time
class KwsSpotter {
 public:
  KwsSpotter(std::unique_ptr<KwsModel> model, KwsFrontend frontend, SpotterPolicy policy);

  // 16 kHz mono samples of any count, 20 ms in audiod; atMs is the caller's time for them. a call the model fails
  // scores nothing and breaks a run of firing calls
  std::optional<Detection> push(std::span<const std::int16_t> pcm20ms, std::int64_t atMs);
  // re-arms as a detection does, without one
  void reset();

 private:
  static_assert(KwsFrontend::kMels == KwsModel::kMels);

  void rearm();
  std::optional<float> call();

  std::unique_ptr<KwsModel> model_;
  KwsFrontend frontend_;
  SpotterPolicy policy_;
  std::array<float, KwsModel::kFrames * KwsModel::kMels> queued_{};
  std::size_t queuedFrames_ = 0;
  // frames since the re-arm, counted up to the first call that ends past rearmFrames
  std::size_t heardFrames_ = 0;
  unsigned firingCalls_ = 0;
};

}  // namespace cadence::kws
