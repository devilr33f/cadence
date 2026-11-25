#pragma once

#include "pulse-shim/abi.h"

#include <poll.h>
#include <sys/time.h>

#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <utility>
#include <variant>
#include <vector>

// the loop's io, time and defer events: their registry, and their dispatch on the loop thread with the lock held
namespace cadence::pulse {

class Loop;

// what an event pointer handed to c points at; the event's state lives in its loop's Events
struct Cell {
  std::shared_ptr<Loop> loop;
  std::uint64_t id = 0;
};

struct Io {
  int fd = -1;
  pa_io_event_flags_t events = 0;
  pa_io_event_cb_t cb = nullptr;
  void* userdata = nullptr;
  pa_io_event_destroy_cb_t destroy = nullptr;
};

struct Time {
  std::optional<timeval> at;
  pa_time_event_cb_t cb = nullptr;
  void* userdata = nullptr;
  pa_time_event_destroy_cb_t destroy = nullptr;
};

struct Defer {
  bool enabled = true;
  pa_defer_event_cb_t cb = nullptr;
  void* userdata = nullptr;
  pa_defer_event_destroy_cb_t destroy = nullptr;
};

template <typename T>
struct Slot {
  std::uint64_t id = 0;
  Cell* cell = nullptr;
  T event;
};

// an event freed by c: its destroy callback runs after the current dispatch, and only then is its cell freed
using Dead = std::variant<Slot<Io>, Slot<Time>, Slot<Defer>>;

struct Watched {
  std::uint64_t id = 0;
  pollfd fd{};
};

struct Events {
  std::uint64_t next = 0;
  std::vector<Slot<Io>> io;
  std::vector<Slot<Time>> time;
  std::vector<Slot<Defer>> defer;
  std::vector<Dead> dead;

  // the io events to poll, and the poll timeout in ms (-1 waits for the fds alone)
  [[nodiscard]] std::pair<std::vector<Watched>, int> pollSet(const timeval& now) const;
  // every event, the dead ones included, to be destroyed
  std::vector<Dead> takeAll();
};

// runs its destroy callback and frees its cell
void destroy(Dead& dead, pa_mainloop_api* api);
// the enabled defers, the due time events and the ready io events, each looked up again right before its call, so
// one an earlier callback freed is skipped; then the dead are destroyed. loop thread, lock held
void dispatch(Loop& loop, const std::vector<std::pair<std::uint64_t, short>>& ready);

// adds an event to the loop behind a and returns the pointer c gets for it; null for a table that is not ours
template <typename T>
void* addEvent(pa_mainloop_api* a, T event);
template <typename T>
void changeEvent(void* e, const std::function<void(T&)>& change);
template <typename T>
void freeEvent(void* e);

}  // namespace cadence::pulse
