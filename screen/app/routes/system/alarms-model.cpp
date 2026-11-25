#include "app/routes/system/alarms-model.h"

#include "i18n/locale.h"

#include <algorithm>

namespace cadence::screen::routes::alarms {

namespace {

bool contains(const std::vector<std::string>& v, const std::string& s) { return std::find(v.begin(), v.end(), s) != v.end(); }

bool local(const Device& d) { return d.here || d.sameRoom; }

}  // namespace

std::optional<Ring> shownRing(const Summary& s, bool awake, const std::vector<std::string>& dismissed) {
  for (const auto& r : s.ringing) {
    if (contains(dismissed, r.key())) continue;
    if (r.own || awake) return r;
  }
  return std::nullopt;
}

bool wakesDisplay(const Summary& s) {
  return std::any_of(s.ringing.begin(), s.ringing.end(), [](const Ring& r) { return r.own; });
}

bool listed(const Summary& s, const std::string& key) {
  return std::any_of(s.ringing.begin(), s.ringing.end(), [&](const Ring& r) { return r.key() == key; });
}

std::optional<Entry> nextAlarm(const Summary& s) {
  std::optional<Entry> best;
  bool bestLocal = false;
  for (const auto& d : s.devices) {
    for (const auto& it : d.items) {
      if (it.kind != "alarm" || it.nextMs <= 0) continue;
      const bool l = local(d);
      // one on this screen or in its room beats any elsewhere; then the earlier
      if (!best || (l && !bestLocal) || (l == bestLocal && it.nextMs < best->item->nextMs)) {
        best = Entry{&d, &it};
        bestLocal = l;
      }
    }
  }
  return best;
}

std::vector<Entry> runningTimers(const Summary& s, std::int64_t nowMs) {
  std::vector<Entry> out;
  for (const auto& d : s.devices) {
    for (const auto& it : d.items) {
      if (it.kind == "timer" && it.nextMs > nowMs) out.push_back(Entry{&d, &it});
    }
  }
  std::stable_sort(out.begin(), out.end(), [](const Entry& a, const Entry& b) {
    const bool la = local(*a.device);
    const bool lb = local(*b.device);
    if (la != lb) return la;
    return a.item->nextMs < b.item->nextMs;
  });
  return out;
}

std::int64_t secondsLeft(std::int64_t endMs, std::int64_t nowMs) {
  const std::int64_t left = endMs - nowMs;
  if (left <= 0) return 0;
  return (left + 999) / 1000;
}

std::string countdownText(std::int64_t endMs, std::int64_t nowMs) { return i18n::countdown(secondsLeft(endMs, nowMs)); }

std::string signature(const Summary& s) {
  std::string sig;
  for (const auto& d : s.devices) {
    sig += d.id + '\x1f' + d.name + '\x1f' + d.room + (d.here ? "h" : "") + (d.sameRoom ? "r" : "") + std::to_string(d.snoozeMinutes) + '\x1e';
    for (const auto& it : d.items) {
      sig += it.kind + '\x1f' + it.id + '\x1f' + it.label + '\x1f' + it.time + '\x1f' + std::to_string(it.nextMs) + '\x1f' +
             std::to_string(it.seconds) + (it.snoozed ? "s" : "") + '\x1f';
      for (const auto& day : it.days) sig += day + ',';
      sig += '\x1d';
    }
  }
  sig += '\x1c';
  for (const auto& r : s.ringing) sig += r.key() + '\x1d';
  return sig;
}

}  // namespace cadence::screen::routes::alarms
