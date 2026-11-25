#pragma once

#include "app/routes/system/chat.h"
#include "ui/core/ui.h"

#include <RmlUi/Core/Element.h>

#include <cstdint>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::screen::routes {

// the chat's elements and their motion: one element per item in a list anchored to the viewport's bottom (so the
// newest sits on the foot and the list grows upward with no scrolling to keep up). layout runs only when an item
// changes; what moves each frame is a transform and an opacity per item: a new item rises 32 dp and fades in, older
// ones glide up as it pushes them and shrink and dim with their age (chat::bottoms closes the room they give up).
// every value is a tween retargeted from where it is, so a change mid-way never jumps; reduced motion snaps the
// transforms and fades opacity alone over 120 ms
class ChatView {
 public:
  struct Spec {
    std::string key;
    // "ci user", "ci reply", …
    std::string cls;
    // the item's content; a card's wrapper (host) keeps what the card host put in it
    std::string rml;
    bool host = false;
    chat::Look look;
    // the item's tweens start where they are (a reload builds them again at rest)
  };

  explicit ChatView(ui::Ui& ui) : ui_(ui) {}

  // the viewport (clips) and the list in it, after a (re)load: what was there is built again at rest
  void attach(Rml::Element* viewport, Rml::Element* list);
  // the items as they should be, oldest first: created, moved, given their content and class, or removed
  void sync(const std::vector<Spec>& specs);
  // what changed the list's layout outside sync (a card placed or replaced): measured again before the next frame
  void touch() { dirty_ = true; }
  [[nodiscard]] Rml::Element* element(std::string_view key) const;
  // an item's opacity now (its words fade up to it)
  [[nodiscard]] double opacity(std::string_view key) const;
  // the item e is in (null when none)
  [[nodiscard]] Rml::Element* itemOf(Rml::Element* e) const;
  // keep e in view: the history moves down until e's top shows; null goes back to the newest
  void reveal(Rml::Element* e);
  // measure, retarget and step; true while anything still moves
  bool tick(std::int64_t nowMs);
  // a conversation reset: the current items fade out before the drawer's next sync replaces them
  void fadeOut();
  // nothing left (the drawer closed)
  void clear();
  [[nodiscard]] std::size_t size() const { return nodes_.size(); }
  // the history's offset (dp-scaled px) the tweens head to
  [[nodiscard]] double offset() const { return offset_; }

 private:
  struct Node {
    std::string key;
    Rml::ObserverPtr<Rml::Element> el;
    std::string cls, rml;
    bool host = false;
    chat::Look look;
    chat::Tween bottom, scale, opacity;
    bool leaving = false;
    bool placed = false;
    std::string transform, alpha;
  };

  Node* find(std::string_view key);
  [[nodiscard]] const Node* find(std::string_view key) const;
  double revealOffset(Rml::Element* e);

  ui::Ui& ui_;
  Rml::ObserverPtr<Rml::Element> viewport_, list_;
  std::vector<Node> nodes_;
  bool dirty_ = false;
  // a reload: the first sync after attach places everything at rest
  bool rest_ = true;
  double offset_ = 0;
  // some item holds words or steps still fading in
  bool fresh_ = false;
  Rml::ObserverPtr<Rml::Element> revealing_;
  std::int64_t now_ = 0;
};

}  // namespace cadence::screen::routes
