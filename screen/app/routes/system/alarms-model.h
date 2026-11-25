#pragma once

#include <cstdint>
#include <optional>
#include <string>
#include <vector>

// the household's alarms, timers and reminders as the screen keeps them (the server's AlarmsSummary, copied out of
// its frame), and the plain choices the ring overlay, home's strip and the alarms page make over them. plain c++, so
// its test runs anywhere. labels, names and rooms are the owner's own text: shown, never logged (rule 8)
namespace cadence::screen::routes::alarms {

struct Item {
  // alarm, timer or reminder (a newer kind is shown by its label and next)
  std::string kind;
  std::string id;
  std::string label;
  // HH:MM in the device's zone, "" for a timer
  std::string time;
  // mon..sun of a repeating one
  std::vector<std::string> days;
  // unix ms it rings next, 0 while unknown
  std::int64_t nextMs = 0;
  std::uint32_t seconds = 0;
  bool snoozed = false;
};

struct Device {
  std::string id;
  std::string name;
  std::string room;
  bool here = false;
  bool sameRoom = false;
  std::vector<Item> items;
  std::uint32_t snoozeMinutes = 0;
};

struct Ring {
  std::string deviceId;
  std::string deviceName;
  std::string room;
  std::string kind;
  std::string id;
  std::string label;
  std::string time;
  std::uint32_t seconds = 0;
  std::int64_t startedMs = 0;
  // this screen's own ring: it wakes the display
  bool own = false;
  std::uint32_t snoozeMinutes = 0;

  // one ring across summaries: its device, kind and id
  [[nodiscard]] std::string key() const { return deviceId + "/" + kind + "/" + id; }
};

struct Summary {
  // this screen's first, then its room's, then the rest (the server's order)
  std::vector<Device> devices;
  std::vector<Ring> ringing;
  std::uint64_t revision = 0;
};

// the minutes a snooze puts a ring off when the summary says none (the device's default)
inline constexpr std::uint32_t kDefaultSnoozeMinutes = 9;

// the ring the overlay shows: the first that is this screen's own, or any once the display is awake; dismissed (a
// stop or a snooze answered ok, by key) are passed over until a summary stops listing them
std::optional<Ring> shownRing(const Summary& s, bool awake, const std::vector<std::string>& dismissed = {});
// the summary rings this screen's own: the display wakes for it (another device's ring never wakes it)
bool wakesDisplay(const Summary& s);
// whether a ring of that key is still listed
bool listed(const Summary& s, const std::string& key);

// a device's item, as home's strip and the page name them
struct Entry {
  const Device* device = nullptr;
  const Item* item = nullptr;
};
// the next alarm: the earliest known next of kind alarm on this screen or in its room, else anywhere
std::optional<Entry> nextAlarm(const Summary& s);
// the timers still running at nowMs, this screen's and its room's first, each group by when it ends
std::vector<Entry> runningTimers(const Summary& s, std::int64_t nowMs);

// whole seconds left until endMs, rounded up (a timer reads 0:01 until it ends), never below 0
std::int64_t secondsLeft(std::int64_t endMs, std::int64_t nowMs);
// 4:05 or 1:02:03 for what is left
std::string countdownText(std::int64_t endMs, std::int64_t nowMs);

// the signature of what the strip and the page draw (a summary that changes nothing they show draws nothing again)
std::string signature(const Summary& s);

}  // namespace cadence::screen::routes::alarms
