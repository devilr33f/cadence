// the alarms model's choices (alarms-model.h): which ring the overlay shows, when the display wakes, home's next
// alarm and its running timers, a timer's countdown. plain c++, so the same test runs in the native simulator and the
// screen suite
#include "app/routes/system/alarms-model.h"

#include <cstdio>
#include <string>

namespace alarms = cadence::screen::routes::alarms;

namespace {

int failures = 0;

void check(bool ok, const std::string& what) {
  if (ok) return;
  ++failures;
  std::printf("fail: %s\n", what.c_str());
}

alarms::Ring ring(const std::string& device, const std::string& id, bool own) {
  alarms::Ring r;
  r.deviceId = device;
  r.kind = "alarm";
  r.id = id;
  r.own = own;
  return r;
}

alarms::Item item(const std::string& kind, const std::string& id, std::int64_t nextMs) {
  alarms::Item i;
  i.kind = kind;
  i.id = id;
  i.nextMs = nextMs;
  return i;
}

void ringChoice() {
  alarms::Summary s;
  check(!alarms::shownRing(s, true), "no ring, nothing shown");
  s.ringing = {ring("kitchen", "a1", false), ring("tv", "a2", true)};
  // awake: the first listed; asleep: only this screen's own
  check(alarms::shownRing(s, true)->id == "a1", "awake shows the first ring");
  check(alarms::shownRing(s, false)->id == "a2", "asleep shows this screen's own ring alone");
  check(alarms::wakesDisplay(s), "an own ring wakes the display");
  s.ringing = {ring("kitchen", "a1", false)};
  check(!alarms::wakesDisplay(s), "another device's ring never wakes it");
  check(!alarms::shownRing(s, false), "another device's ring stays hidden while asleep");
  check(alarms::shownRing(s, true).has_value(), "and shows once awake while it still rings");
  // a stop answered ok hides it until a summary drops it
  check(!alarms::shownRing(s, true, {"kitchen/alarm/a1"}), "a dismissed ring is passed over");
  check(alarms::listed(s, "kitchen/alarm/a1") && !alarms::listed(s, "kitchen/alarm/a9"), "listed by key");
}

void nextAndTimers() {
  alarms::Summary s;
  alarms::Device far;
  far.id = "bedroom";
  far.items = {item("alarm", "early", 1000), item("timer", "t-far", 50000)};
  alarms::Device near;
  near.id = "living";
  near.sameRoom = true;
  near.items = {item("alarm", "later", 5000), item("alarm", "unknown", 0), item("timer", "t-done", 900),
                item("timer", "t-near", 90000), item("reminder", "r", 100)};
  s.devices = {far, near};
  const auto next = alarms::nextAlarm(s);
  check(next && next->item->id == "later", "the room's alarm beats an earlier one elsewhere");
  s.devices[1].items.clear();
  check(alarms::nextAlarm(s)->item->id == "early", "else the earliest anywhere");
  s.devices = {far, near};
  const auto timers = alarms::runningTimers(s, 1000);
  check(timers.size() == 2, "ended timers are not running");
  check(timers.size() == 2 && timers[0].item->id == "t-near" && timers[1].item->id == "t-far",
        "the room's timers first");
}

void countdowns() {
  check(alarms::secondsLeft(10000, 9001) == 1, "a part second rounds up");
  check(alarms::secondsLeft(10000, 10000) == 0 && alarms::secondsLeft(10000, 20000) == 0, "never below zero");
  check(alarms::countdownText(10000 + 245000, 10000) == "4:05", "4:05 left");
  check(alarms::countdownText(3723000, 0) == "1:02:03", "hours");
}

void signature() {
  alarms::Summary a;
  alarms::Device d;
  d.id = "tv";
  d.items = {item("timer", "t", 5000)};
  a.devices = {d};
  auto b = a;
  b.revision = 7;
  check(alarms::signature(a) == alarms::signature(b), "a new revision alone draws nothing again");
  b.devices[0].items[0].snoozed = true;
  check(alarms::signature(a) != alarms::signature(b), "a snooze does");
}

}  // namespace

int main() {
  ringChoice();
  nextAndTimers();
  countdowns();
  signature();
  if (failures != 0) return 1;
  std::printf("alarms-model: ok\n");
  return 0;
}
