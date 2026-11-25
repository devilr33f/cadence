#pragma once

#include "app/routes/system/device.h"
#include "app/routes/system/env.h"
#include "app/routes/system/page-rpc.h"
#include "ui/cards/host.h"

#include <functional>
#include <memory>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// background tasks on the screen (design r2 section 20), drawn from the server's cadence cards alone (the device
// knows nothing about hermes): the top bar's indicator from the server's tasks_summary (the list's card tones only
// for a cadenced that sends none), the tasks panel (the right
// sheet: waiting, then running, then the finished under a divider) and a task's panel one level deeper (tasks.item,
// subscribed; or a list card's «Открыть»). a new revision updates in place without moving focus or scroll: the live
// tail follows its end until the reader scrolls up, then a «новое ↓» pill says there is more. an approval opens with
// focus on «Запретить», and both of its buttons ask first (the confirm's own focus is on the safe choice)
class Tasks {
 public:
  Tasks(Env env, PageRpc& rpc, const DeviceState& device);
  ~Tasks();
  Tasks(const Tasks&) = delete;
  Tasks& operator=(const Tasks&) = delete;

  // link_status starts and stops the feed; route TASKS and TASK open the panels
  bool message(const cadence_screen_v1_Envelope& m);

  void openList();
  // a task by the ref the server issued (Route.ref, a toast's «Открыть»)
  void openTask(const std::string& ref);
  void close();
  [[nodiscard]] bool isOpen() const { return level_ != Level::Closed; }
  [[nodiscard]] bool inTask() const { return level_ == Level::Task; }
  [[nodiscard]] const std::string& taskRef() const { return taskRef_; }
  [[nodiscard]] cards::CardHost& cards() { return *cards_; }
  // the «новое ↓» pill is showing
  [[nodiscard]] bool newBelow() const { return newBelow_; }

  // the indicator: open (running or waiting) and waiting tasks, and those finished since the page was last opened
  [[nodiscard]] int active() const { return active_; }
  [[nodiscard]] int waiting() const { return waiting_; }
  [[nodiscard]] int finished() const { return finished_; }
  // the top bar's indicator (left of search on home), empty with nothing open
  [[nodiscard]] std::string indicator() const;
  std::function<void()> onIndicatorChanged;
  // before the panel takes focus (the other sheet closes first); and once it opened or closed
  std::function<void()> onOpening;
  std::function<void(bool open)> onOpenChanged;

  // the drawer over the panel: its duck stands in for the panel's scrim (two translucent layers at most)
  void setUnderDrawer(bool under);
  // a page the server answered (tests and the gallery feed it here as the link would)
  void showList(const cadence_cards_v1_Page& page);
  void showTask(const cadence_cards_v1_Page& page, bool live);

 private:
  enum class Level { Closed, List, Task };

  std::string build();
  void loaded(Rml::ElementDocument& doc);
  bool key(const platform::KeyEvent& e);
  bool activate(const ui::Activation& a);
  void tick(std::int64_t nowMs);

  void subscribe();
  void count(const cadence_cards_v1_Page& page);
  void summary(const cadence_v2_TasksSummary& summary);
  void setCounts(int active, int waiting, int finished);
  void openLevel(Level l);
  // the page's cards in place (a new revision replaces its card), keeping focus and the tails' scroll
  void update(const cadence_cards_v1_Page& page);
  void draw(const cadence_cards_v1_Page& page);
  void divider();
  void focusInitial();
  void focusNow();
  void watch();
  [[nodiscard]] Rml::Element* el(const char* id) const;

  Env env_;
  Alive alive_;
  PageRpc& rpc_;
  const DeviceState& device_;
  std::unique_ptr<cards::CardHost> cards_;
  std::vector<int> hooks_;
  Rml::ElementDocument* doc_ = nullptr;

  Level level_ = Level::Closed;
  std::string feedId_;
  std::string itemId_;
  std::string watchId_;
  std::string taskRef_;
  // the list card the task was opened from: BACK focuses it again
  std::string openerCard_;
  cards::PagePtr list_;
  cards::PagePtr task_;
  int active_ = 0;
  int waiting_ = 0;
  int finished_ = 0;
  // a tasks_summary came: its counts are the indicator's from then on
  bool summarized_ = false;
  std::uint64_t summaryRevision_ = 0;
  bool under_ = false;
  bool newBelow_ = false;
  bool following_ = true;
};

}  // namespace cadence::screen::routes
