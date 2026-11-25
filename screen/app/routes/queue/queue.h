#pragma once

#include "app/routes/panels/side-sheet.h"

#include <functional>
#include <optional>
#include <vector>

namespace cadence::screen::routes {

// the queue (design §15): the same 720 dp side sheet, one list in three groups, focus on the current item. history
// (this session's only, collapsed to its last 3 behind «Вся история»), now (128 dp, art, a progress line, three static
// equaliser bars), next (each episode by its number and name, the show's art where it has none of its own). OK on
// any item jumps to it; long OK: play now, play next, remove. its page comes from media.queue {sessionId}; until the server answers it (or where it has none) the queue is what the session
// knows: the item, then the server's up next or the episodes after it. OK on a server item is media.control's jump,
// long OK's play next and remove are media.control's play_next and remove
class QueueContent final : public SheetContent {
 public:
  // jumps to an item (the routes resolve and play it); the sheet closes 300 ms after the new item starts
  using Jump = std::function<void(const QueueItem& item)>;
  explicit QueueContent(Jump jump) : jump_(std::move(jump)) {}

  [[nodiscard]] std::string title() const override { return "[[audio.queue]]"; }
  [[nodiscard]] cadence_v2_ScreenRoute route() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_QUEUE; }
  void open(SideSheet& sheet, Rml::Element* body) override;
  bool activate(const ui::Activation& a) override;

  // the gallery and tests: a page as if media.queue had answered
  void show(const QueuePage& page);
  [[nodiscard]] const QueuePage& page() const { return page_; }

  // what the session alone knows: the current item and the episodes after it
  static QueuePage fromSession(const Session& s);

  // an item's two lines, as rml: an episode reads «Серия 7» with its name (else its subtitle) under it; its own
  // title only when it is a name and not the bare number the catalog gives an unnamed episode
  struct Line {
    std::string title;
    std::string meta;
  };
  static Line line(const QueueItem& q);

 private:
  void render();
  // media.queue's page for the session
  void load();
  const QueueItem* item(const std::string& key) const;

  Jump jump_;
  SideSheet* sheet_ = nullptr;
  Rml::ObserverPtr<Rml::Element> body_;
  QueuePage page_;
  bool allHistory_ = false;
};

}  // namespace cadence::screen::routes
