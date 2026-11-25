#include "app/routes/system/reel.h"

#include <algorithm>

namespace cadence::screen::routes::reel {

Pace pace(std::size_t waiting, bool hurry) {
  // with nothing or one card behind, a card reads at leisure; each more one waiting shortens both
  Pace p;
  switch (waiting) {
    case 0:
    case 1:
      p = {3200, 420};
      break;
    case 2:
      p = {1600, 300};
      break;
    default:
      p = {800, 220};
      break;
  }
  if (hurry) {
    p.dwellMs = std::max<std::int64_t>(500, p.dwellMs / 2);
    p.slideMs = std::min<std::int64_t>(p.slideMs, 260);
  }
  return p;
}

bool Reel::holds(std::string_view id) const {
  return current_ == id || std::find(history_.begin(), history_.end(), id) != history_.end() ||
         std::find(waiting_.begin(), waiting_.end(), id) != waiting_.end();
}

void Reel::toHistory(std::string id) {
  if (id.empty()) return;
  history_.push_front(std::move(id));
  while (history_.size() > kMaxHistory) history_.pop_back();
}

void Reel::passOver(std::string id) {
  // never current, so never the one just before it: behind the whole history
  if (id.empty() || history_.size() >= kMaxHistory) return;
  history_.push_back(std::move(id));
}

void Reel::advance(std::string next, std::int64_t nowMs) {
  toHistory(std::move(current_));
  current_ = std::move(next);
  shownMs_ = nowMs;
  slideMs_ = pace(waiting_.size(), hurry_).slideMs;
}

void Reel::push(const std::string& id, std::int64_t nowMs) {
  if (id.empty() || holds(id)) return;
  if (current_.empty()) {
    current_ = id;
    shownMs_ = nowMs;
    slideMs_ = pace(0, hurry_).slideMs;
    return;
  }
  waiting_.push_back(id);
  // never more than kMaxLag behind: the oldest waiting are passed over into the history unseen as current
  while (waiting_.size() > kMaxLag) {
    passOver(std::move(waiting_.front()));
    waiting_.pop_front();
  }
}

void Reel::remove(std::string_view id) {
  std::erase(history_, id);
  std::erase(waiting_, id);
  if (current_ != id) return;
  current_.clear();
  if (!waiting_.empty()) {
    current_ = std::move(waiting_.front());
    waiting_.pop_front();
  } else if (!history_.empty()) {
    current_ = std::move(history_.front());
    history_.pop_front();
  }
}

bool Reel::front(std::string_view id, std::int64_t nowMs) {
  if (id.empty()) return false;
  if (current_ == id) {
    while (!waiting_.empty()) {
      passOver(std::move(waiting_.front()));
      waiting_.pop_front();
    }
    return true;
  }
  std::string next(id);
  if (auto it = std::find(waiting_.begin(), waiting_.end(), id); it != waiting_.end()) {
    waiting_.erase(it);
  } else if (auto h = std::find(history_.begin(), history_.end(), id); h != history_.end()) {
    history_.erase(h);
  } else {
    return false;
  }
  advance(std::move(next), nowMs);
  // the cited card is what is talked about: nothing replaces it
  while (!waiting_.empty()) {
    passOver(std::move(waiting_.front()));
    waiting_.pop_front();
  }
  return true;
}

std::int64_t Reel::dueMs() const {
  if (waiting_.empty()) return -1;
  return shownMs_ + pace(waiting_.size(), hurry_).dwellMs;
}

bool Reel::tick(std::int64_t nowMs) {
  const auto due = dueMs();
  if (due < 0 || nowMs < due) return false;
  auto next = std::move(waiting_.front());
  waiting_.pop_front();
  advance(std::move(next), nowMs);
  return true;
}

void Reel::clear() {
  current_.clear();
  history_.clear();
  waiting_.clear();
  shownMs_ = 0;
  slideMs_ = 0;
  hurry_ = false;
}

}  // namespace cadence::screen::routes::reel
