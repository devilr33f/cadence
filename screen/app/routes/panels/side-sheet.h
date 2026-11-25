#pragma once

#include "app/routes/playback/context.h"

#include <RmlUi/Core/ObserverPtr.h>

#include <memory>
#include <string>

namespace cadence::screen::routes {

class SideSheet;

// what a side sheet holds: a panel's cards, the queue, the voiceover switcher
class SheetContent {
 public:
  virtual ~SheetContent() = default;
  [[nodiscard]] virtual std::string title() const = 0;
  [[nodiscard]] virtual cadence_v2_ScreenRoute route() const = 0;
  // fills body (the sheet's .sbody) and starts what it loads
  virtual void open(SideSheet& sheet, Rml::Element* body) = 0;
  virtual bool activate(const ui::Activation& a) {
    (void)a;
    return false;
  }
  // a message of the link (a card host's own answers)
  virtual bool message(const cadence_screen_v1_Envelope& m) {
    (void)m;
    return false;
  }
  virtual void tick(std::int64_t nowMs) { (void)nowMs; }
  // focus moved inside the sheet
  virtual void focused(Rml::Element* e) { (void)e; }
};

// the right side sheet of design §14-15 and §18.6: 720 dp, full height, opaque, sliding 48 dp in from the right
// (320 ms); the film plays on, unscrimmed, at the left. a focus scope of its own (the sheet is modal: ← at its edge
// does nothing); ↑/↓ move between its focusables and it scrolls so focus sits in the upper third. BACK closes it in
// 220 ms and gives focus back to whatever opened it
class SideSheet {
 public:
  explicit SideSheet(PlaybackContext& c) : c_(c) {}

  // into the page's #side-host; opener gets focus back on close
  void open(std::unique_ptr<SheetContent> content, Rml::Element* opener);
  void close();
  [[nodiscard]] bool isOpen() const { return content_ != nullptr; }
  [[nodiscard]] SheetContent* content() const { return content_.get(); }

  bool key(const platform::KeyEvent& e);
  bool activate(const ui::Activation& a);
  bool message(const cadence_screen_v1_Envelope& m);
  void tick(std::int64_t nowMs);

  // keeps the focused element in the upper third of the sheet's body
  void scrollTo(Rml::Element* e);
  [[nodiscard]] PlaybackContext& context() { return c_; }
  // after a close (the page shows its osd again for 3 s)
  std::function<void()> onClosed;

 private:
  PlaybackContext& c_;
  std::unique_ptr<SheetContent> content_;
  Rml::ObserverPtr<Rml::Element> layer_;
  Rml::ObserverPtr<Rml::Element> opener_;
  float scroll_ = 0;
};

}  // namespace cadence::screen::routes
