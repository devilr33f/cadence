#pragma once

#include "app/routes/playback/context.h"

#include <cstdint>
#include <optional>
#include <string>

namespace cadence::screen::routes {

// the audio page's playlist sidebar: the server queue's collection and whole play order, the current row marked. no
// answer, a cast or a single url leaves no element and no queued layout; a row jumps through media.control by its ref
class AudioQueueSidebar {
 public:
  explicit AudioQueueSidebar(PlaybackContext& c) : c_(c) {}

  void enter();
  void load(bool force = false);
  [[nodiscard]] bool visible() const { return queue_ && !queue_->items.empty(); }
  [[nodiscard]] std::string body() const;
  void render();
  bool activate(const ui::Activation& a);
  // after the focus manager moves: the fixed sidebar scrolls itself, not the audio page
  void keepVisible();

 private:
  void scrollTo(Rml::Element* e);

  PlaybackContext& c_;
  std::optional<AudioQueue> queue_;
  std::string sessionId_;
  std::uint64_t generation_ = 0;
  bool asked_ = false;
  float scroll_ = 0;
};

}  // namespace cadence::screen::routes
