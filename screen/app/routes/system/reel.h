#pragma once

#include <cstddef>
#include <cstdint>
#include <deque>
#include <string>
#include <string_view>
#include <vector>

// the showcase's reel (the owner's idea): content cards the agent's tools return slide in on the left of the screen
// while the drawer talks on the right. one card is current; the ones before it step back and leave; new ones wait
// their turn. the pace adapts to the queue: with one card waiting the current stays readable for a few seconds; as
// cards pile up (the agent keeps searching) each stays less and slides faster, and past kMaxLag waiting the oldest
// waiting pass straight into the history, so the reel is never more than a couple of cards behind. once nothing
// waits the last card stays. nothing here touches rmlui, so the tests read it plainly
namespace cadence::screen::routes::reel {

struct Pace {
  // how long the current card stays before the next replaces it
  std::int64_t dwellMs = 0;
  // how long the next one takes to slide in (and the current to step back)
  std::int64_t slideMs = 0;
};

// at most this many cards wait behind the current one
inline constexpr std::size_t kMaxLag = 2;
// the cards kept behind the current one (the view shows two of them)
inline constexpr std::size_t kMaxHistory = 8;

// the pace for `waiting` cards behind the current one; hurry (the reply is being spoken) halves the dwell so the
// newest card is up while it is talked about
Pace pace(std::size_t waiting, bool hurry);

class Reel {
 public:
  // a new card: current at once on an empty reel, else it waits (an id already held keeps its place)
  void push(const std::string& id, std::int64_t nowMs);
  // a card that went (its ttl, the drawer's close)
  void remove(std::string_view id);
  // a card the reply cites comes to the front now and stays: what waited goes into the history unseen as current;
  // false when the reel does not hold it
  bool front(std::string_view id, std::int64_t nowMs);
  void hurry(bool on) { hurry_ = on; }
  // advances the reel; true when the current card changed
  bool tick(std::int64_t nowMs);
  void clear();

  [[nodiscard]] const std::string& current() const { return current_; }
  // the cards shown before the current one, newest first, then those passed over unseen
  [[nodiscard]] const std::deque<std::string>& history() const { return history_; }
  [[nodiscard]] const std::deque<std::string>& waiting() const { return waiting_; }
  [[nodiscard]] bool holds(std::string_view id) const;
  [[nodiscard]] bool empty() const { return current_.empty(); }
  // the slide that brought the current card (the view's transition), and when it started
  [[nodiscard]] std::int64_t slideMs() const { return slideMs_; }
  [[nodiscard]] std::int64_t shownMs() const { return shownMs_; }
  // when the next card is due (-1 when nothing waits)
  [[nodiscard]] std::int64_t dueMs() const;

 private:
  void advance(std::string next, std::int64_t nowMs);
  void toHistory(std::string id);
  // a card passed over unseen: at the history's far end
  void passOver(std::string id);

  std::string current_;
  std::deque<std::string> history_;
  std::deque<std::string> waiting_;
  std::int64_t shownMs_ = 0;
  std::int64_t slideMs_ = 0;
  bool hurry_ = false;
};

}  // namespace cadence::screen::routes::reel
