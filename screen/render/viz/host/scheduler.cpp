#include "scheduler.h"

#include <algorithm>
#include <utility>

namespace cadence::render::viz {

double Scheduler::progress(double now_s) const {
  if (started_ < 0 || c_.transition_s <= 0) return 1;
  return std::clamp((now_s - started_) / c_.transition_s, 0.0, 1.0);
}

std::optional<Scheduler::Move> Scheduler::step(double now_s, bool beat) {
  if (since_ < 0) since_ = now_s;
  std::optional<Move> move;
  if (asked_) {
    move = std::exchange(asked_, std::nullopt);
  } else if (auto_ && !transitioning(now_s)) {
    const double played = now_s - since_;
    if ((beat && played >= c_.every_s) || played >= c_.every_s + c_.max_wait_s) move = Move::Next;
  }
  if (move) {
    started_ = now_s;
    since_ = now_s;
  }
  return move;
}

}  // namespace cadence::render::viz
