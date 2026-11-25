#pragma once

#include "cadence/screen/v1/screen.pb.h"
#include "ui/core/ui.h"

#include <cstdint>
#include <memory>

namespace Rml {
class Element;
class ElementDocument;
}  // namespace Rml

namespace cadence::screen::ui {

// the volume overlay: an upright pill at the screen's right edge with the level as a bar and a number, and the mute,
// shown for each VolumeState cadenced sends with show set (the level in force after cadenced applied it, whoever set
// it) and hidden kVolumeShowMs after the last. a layer of its own over the page and the screens' layers, below toasts
// and dialogs; it holds nothing focusable and takes no key, so focus stays where it was
class VolumeOverlay {
 public:
  static constexpr std::int64_t kVolumeShowMs = 2000;
  static constexpr int kZ = 60;

  explicit VolumeOverlay(Ui& ui);
  ~VolumeOverlay();
  VolumeOverlay(const VolumeOverlay&) = delete;
  VolumeOverlay& operator=(const VolumeOverlay&) = delete;

  // every envelope; it acts on volume_state alone
  void message(const cadence_screen_v1_Envelope& m);
  // what the screen shows now: for the tests and the sim
  [[nodiscard]] bool visible() const { return visible_; }
  [[nodiscard]] int level() const { return level_; }
  [[nodiscard]] bool muted() const { return muted_; }

 private:
  void show(int level, bool muted);
  void loaded(Rml::ElementDocument& doc);
  void render(bool animate);
  void enter();
  void hide(std::uint64_t seq);
  Rml::Element* el(const char* id) const;

  Ui& ui_;
  Rml::ElementDocument* doc_ = nullptr;
  int level_ = 0;
  bool muted_ = false;
  bool visible_ = false;
  // the latest show: a hide timer of an earlier one does nothing
  std::uint64_t seq_ = 0;
  // the ui's timers may outlive the overlay
  std::shared_ptr<int> alive_ = std::make_shared<int>(0);
};

}  // namespace cadence::screen::ui
