#pragma once

#include <cstddef>
#include <optional>

namespace cadence::render::viz {

struct SchedulerConfig {
  // a source plays at least this long before a beat may switch it
  double every_s = 20;
  // with no beat by then, it switches anyway
  double max_wait_s = 8;
  // a transition's length
  double transition_s = 2;
};

// when the host moves from one source to the next: on a beat once a source has played every_s (or max_wait_s
// later without one), and at once on ← and →. a transition runs transition_s; nothing switches during one, but a
// key during one jumps to its end first
class Scheduler {
 public:
  enum class Move { Next, Prev };

  explicit Scheduler(SchedulerConfig c = {}) : c_(c) {}

  // a frame at now_s; returns the move to start, if any
  std::optional<Move> step(double now_s, bool beat);
  // ← or →: the move starts on the next step
  void ask(Move m) { asked_ = m; }
  // a move began at now_s by other means (a new track, a slow source skipped): its transition runs from here
  void started(double now_s) {
    started_ = now_s;
    since_ = now_s;
  }
  // auto switching off (paused, reduced motion, one source)
  void set_auto(bool on) { auto_ = on; }

  [[nodiscard]] bool transitioning(double now_s) const { return started_ >= 0 && now_s - started_ < c_.transition_s; }
  // 0..1 through the running transition (1 when none)
  [[nodiscard]] double progress(double now_s) const;
  [[nodiscard]] const SchedulerConfig& config() const { return c_; }

 private:
  SchedulerConfig c_;
  std::optional<Move> asked_;
  bool auto_ = true;
  double since_ = -1;
  double started_ = -1;
};

}  // namespace cadence::render::viz
