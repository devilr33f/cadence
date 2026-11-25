#pragma once

#include <RmlUi/Core/Element.h>
#include <RmlUi/Core/ObserverPtr.h>

#include <cstdint>
#include <functional>
#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::ui {

enum class Dir { Up, Down, Left, Right };

// a box as the viewer sees it: layout position less the row and page scroll the focus manager applied
struct VisualRect {
  float x = 0, y = 0, w = 0, h = 0;
  [[nodiscard]] float right() const { return x + w; }
  [[nodiscard]] float bottom() const { return y + h; }
  [[nodiscard]] float cx() const { return x + w / 2; }
  [[nodiscard]] float cy() const { return y + h / 2; }
};

// the d-pad's focus (design r2 section 7). rmlui's own spatial navigation is off (".f { nav: none }"): this moves
// focus, so a row can keep column memory and anchored scrolling, which nav: auto's nearest geometry cannot.
//
// markup it reads:
//   .f        focusable (and nothing else is)
//   .row      a row of cards: ←/→ stay inside it (no wrap: a 12 dp nudge at an end), ↑/↓ into it restore the item it
//             last had focused (data-last), or on a first visit the item nearest in x; its .track scrolls so focus
//             sits in slot 2 from the second item on, and its one .cline slides under the focused item's .title
//   .vscroll  the page's column: scrolled so the focused row's last line ends at the title-safe bottom (.manual: the
//             page scrolls it itself, keeping data-scroll for rect())
//   .default-focus  where focus goes when nothing else can take it
// a scope is the page or an open level (sheet, dialog, keyboard); closing one gives focus back to its opener
class Focus {
 public:
  // a callback when focus moved (the gallery's walk, the screen context's focused ref)
  using Listener = std::function<void(Rml::Element*)>;

  void setDpRatio(float ratio) { dp_ = ratio; }
  // the page's root (its document's body); clears every scope but those keep says to keep (a layer's own: the
  // drawer stays focused while the page under it changes)
  void setRoot(Rml::Element* root, const std::function<bool(Rml::Element*)>& keep = {});
  void pushScope(Rml::Element* scope);
  // closes the top scope and focuses its opener (or repairs); false when only the page is left
  bool popScope();
  // closes the scope rooted at scope wherever it sits (a layer that goes while another is open over it): its
  // opener is kept for the scope above it, or focused when it was the top one; false when there is none
  bool dropScope(Rml::Element* scope);
  [[nodiscard]] Rml::Element* scope() const;
  [[nodiscard]] std::size_t depth() const { return scopes_.size(); }

  // the focusable elements of the scope, visible, in document order
  [[nodiscard]] std::vector<Rml::Element*> candidates() const;
  [[nodiscard]] Rml::Element* current() const { return current_.get(); }

  // a d-pad key: true when focus moved or nudged; false when up or down found nothing (a toast below takes it)
  bool move(Dir dir);
  // focuses e: row memory, row scroll, the cadence line, page scroll
  void focus(Rml::Element* e);
  // focus never lost (section 7): a removed or hidden focused element hands focus to its next sibling, else its
  // previous one, else the nearest in its row, else the row above, else the default
  void repair();
  // held keys after the third repeat: 80 ms transitions, short glides
  void setHeld(bool held);
  // reduced motion: every glide 120 ms at most
  void setReduced(bool on) { reduced_ = on; }
  // steps the nudges and the cadence line's entry; true while one is running
  bool tick(std::int64_t nowMs);
  // ms until tick has something to step (0: now), -1 when nothing runs
  [[nodiscard]] std::int64_t nextDueMs(std::int64_t nowMs) const;

  void onFocus(Listener l) { listener_ = std::move(l); }

  [[nodiscard]] VisualRect rect(Rml::Element* e) const;
  // a scroll container's transform eased there (cubic out) from wherever it is now: an animation, not a transition
  // (rmlui transitions only follow class changes, so an inline transform jumped), retargeted by the next call with no
  // jump, shorter while a key is held. transform alone, never a layout. ms 0: there at once
  void glide(Rml::Element* e, const std::string& transform, int ms) const;
  // the durations it uses: a row's track, the page's column (both shortened while held)
  [[nodiscard]] int rowGlideMs() const;
  [[nodiscard]] int columnGlideMs() const;
  // nearest ancestor (or self) with the class, inside the scope
  static Rml::Element* ancestor(Rml::Element* e, const char* cls);

 private:
  struct Scope {
    Rml::ObserverPtr<Rml::Element> root;
    Rml::ObserverPtr<Rml::Element> opener;
  };
  struct Nudge {
    Rml::ObserverPtr<Rml::Element> track;
    std::int64_t untilMs = 0;
    bool started = false;
    float dx = 0;
  };

  std::vector<Rml::Element*> rowItems(Rml::Element* row) const;
  Rml::Element* entryOf(Rml::Element* row, const VisualRect& from) const;
  void scrollRow(Rml::Element* row, Rml::Element* item);
  void placeLine(Rml::Element* row, Rml::Element* item, bool entering);
  void scrollPage(Rml::Element* e);
  void nudge(Rml::Element* e, Dir dir);
  void remember(Rml::Element* e);
  void prune();

  float dp_ = 1.0f;
  std::int64_t nowMs_ = 0;
  bool held_ = false;
  bool reduced_ = false;
  Rml::ObserverPtr<Rml::Element> root_;
  std::vector<Scope> scopes_;
  Rml::ObserverPtr<Rml::Element> current_;
  // where the focused element sat, for repair
  Rml::ObserverPtr<Rml::Element> parent_;
  int index_ = -1;
  Rml::ObserverPtr<Rml::Element> row_;
  std::vector<Nudge> nudges_;
  // cadence lines entering: placed at the item's centre with scale 0 this frame, at the title next frame
  struct Entry {
    Rml::ObserverPtr<Rml::Element> row;
    Rml::ObserverPtr<Rml::Element> item;
  };
  std::vector<Entry> entries_;
  Listener listener_;
};

}  // namespace cadence::screen::ui
