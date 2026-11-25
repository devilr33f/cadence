#include "pulse-shim/events.h"

#include "pulse-shim/mainloop.h"
#include "pulse-shim/util.h"

#include <algorithm>
#include <climits>

namespace cadence::pulse {

namespace {

template <typename T>
std::vector<Slot<T>>& listOf(Events& events);

template <>
std::vector<Slot<Io>>& listOf<Io>(Events& events) {
  return events.io;
}

template <>
std::vector<Slot<Time>>& listOf<Time>(Events& events) {
  return events.time;
}

template <>
std::vector<Slot<Defer>>& listOf<Defer>(Events& events) {
  return events.defer;
}

template <typename T>
Slot<T>* find(std::vector<Slot<T>>& list, std::uint64_t id) {
  const auto it = std::find_if(list.begin(), list.end(), [id](const Slot<T>& slot) { return slot.id == id; });
  return it == list.end() ? nullptr : &*it;
}

int msUntil(const timeval& at, const timeval& now) {
  const auto remaining = usec(at) - usec(now);
  if (remaining <= 0)
    return 0;
  const auto ms = (remaining + 999) / 1000;
  return ms > INT_MAX ? INT_MAX : static_cast<int>(ms);
}

short pollEvents(pa_io_event_flags_t events) {
  short out = 0;
  if ((events & PA_IO_EVENT_INPUT) != 0)
    out = static_cast<short>(out | POLLIN);
  if ((events & PA_IO_EVENT_OUTPUT) != 0)
    out = static_cast<short>(out | POLLOUT);
  return out;
}

pa_io_event_flags_t ioFlags(short revents) {
  pa_io_event_flags_t flags = 0;
  if ((revents & POLLIN) != 0)
    flags |= PA_IO_EVENT_INPUT;
  if ((revents & POLLOUT) != 0)
    flags |= PA_IO_EVENT_OUTPUT;
  if ((revents & POLLHUP) != 0)
    flags |= PA_IO_EVENT_HANGUP;
  if ((revents & (POLLERR | POLLNVAL)) != 0)
    flags |= PA_IO_EVENT_ERROR;
  return flags;
}

bool due(const Slot<Time>& slot, const timeval& now) { return slot.event.at && usec(*slot.event.at) <= usec(now); }

// the pointer c was given for an event
pa_io_event* cPointer(const Slot<Io>& slot) { return reinterpret_cast<pa_io_event*>(slot.cell); }
pa_time_event* cPointer(const Slot<Time>& slot) { return reinterpret_cast<pa_time_event*>(slot.cell); }
pa_defer_event* cPointer(const Slot<Defer>& slot) { return reinterpret_cast<pa_defer_event*>(slot.cell); }

}  // namespace

std::pair<std::vector<Watched>, int> Events::pollSet(const timeval& now) const {
  std::vector<Watched> watched;
  for (const auto& slot : io)
    if (slot.event.events != 0)
      watched.push_back({slot.id, pollfd{slot.event.fd, pollEvents(slot.event.events), 0}});
  int timeout = -1;
  if (!dead.empty() || std::any_of(defer.begin(), defer.end(), [](const auto& slot) { return slot.event.enabled; })) {
    timeout = 0;
  } else {
    for (const auto& slot : time)
      if (slot.event.at) {
        const auto ms = msUntil(*slot.event.at, now);
        timeout = timeout < 0 ? ms : std::min(timeout, ms);
      }
  }
  return {std::move(watched), timeout};
}

std::vector<Dead> Events::takeAll() {
  std::vector<Dead> all;
  for (auto& slot : io)
    all.emplace_back(std::move(slot));
  for (auto& slot : time)
    all.emplace_back(std::move(slot));
  for (auto& slot : defer)
    all.emplace_back(std::move(slot));
  for (auto& slot : dead)
    all.push_back(std::move(slot));
  io.clear();
  time.clear();
  defer.clear();
  dead.clear();
  return all;
}

void destroy(Dead& dead, pa_mainloop_api* api) {
  Cell* cell = std::visit(
      [api](auto& slot) {
        if (slot.event.destroy != nullptr)
          slot.event.destroy(api, cPointer(slot), slot.event.userdata);
        return slot.cell;
      },
      dead);
  delete cell;
}

void dispatch(Loop& loop, const std::vector<std::pair<std::uint64_t, short>>& ready) {
  auto* api = loop.api();
  const auto defers = loop.withEvents([](Events& events) {
    std::vector<std::uint64_t> ids;
    for (const auto& slot : events.defer)
      if (slot.event.enabled)
        ids.push_back(slot.id);
    return ids;
  });
  for (const auto id : defers) {
    const auto call = loop.withEvents([id](Events& events) -> std::optional<Slot<Defer>> {
      const auto* slot = find(events.defer, id);
      if (slot == nullptr || !slot->event.enabled)
        return std::nullopt;
      return *slot;
    });
    if (call && call->event.cb != nullptr)
      call->event.cb(api, cPointer(*call), call->event.userdata);
  }

  const auto current = now();
  const auto expired = loop.withEvents([&](Events& events) {
    std::vector<std::uint64_t> ids;
    for (const auto& slot : events.time)
      if (due(slot, current))
        ids.push_back(slot.id);
    return ids;
  });
  for (const auto id : expired) {
    // a time event fires once per time it was given: taken here, so a restart arms it again
    const auto call = loop.withEvents([&](Events& events) -> std::optional<std::pair<Slot<Time>, timeval>> {
      auto* slot = find(events.time, id);
      if (slot == nullptr || !due(*slot, current))
        return std::nullopt;
      const auto at = *slot->event.at;
      slot->event.at.reset();
      return std::pair{*slot, at};
    });
    if (call && call->first.event.cb != nullptr)
      call->first.event.cb(api, cPointer(call->first), &call->second, call->first.event.userdata);
  }

  for (const auto& [id, revents] : ready) {
    const auto call = loop.withEvents([&](Events& events) -> std::optional<std::pair<Slot<Io>, pa_io_event_flags_t>> {
      const auto* slot = find(events.io, id);
      if (slot == nullptr || slot->event.events == 0)
        return std::nullopt;
      const auto wanted = slot->event.events | PA_IO_EVENT_HANGUP | PA_IO_EVENT_ERROR;
      return std::pair{*slot, ioFlags(revents) & wanted};
    });
    if (call && call->first.event.cb != nullptr && call->second != 0)
      call->first.event.cb(api, cPointer(call->first), call->first.event.fd, call->second, call->first.event.userdata);
  }

  auto dead = loop.withEvents([](Events& events) { return std::exchange(events.dead, {}); });
  for (auto& event : dead)
    destroy(event, api);
}

template <typename T>
void* addEvent(pa_mainloop_api* a, T event) {
  const auto loop = Loop::fromApi(a);
  if (!loop)
    return nullptr;
  auto* cell = new Cell{loop, 0};
  loop->withEvents([&](Events& events) {
    cell->id = ++events.next;
    listOf<T>(events).push_back(Slot<T>{cell->id, cell, std::move(event)});
  });
  loop->interrupt();
  return cell;
}

template <typename T>
void changeEvent(void* e, const std::function<void(T&)>& change) {
  if (e == nullptr)
    return;
  auto* cell = static_cast<Cell*>(e);
  cell->loop->withEvents([&](Events& events) {
    if (auto* slot = find(listOf<T>(events), cell->id))
      change(slot->event);
  });
  cell->loop->interrupt();
}

template <typename T>
void freeEvent(void* e) {
  if (e == nullptr)
    return;
  auto* cell = static_cast<Cell*>(e);
  // the cell stays valid until its destroy callback ran: keep the loop it names alive across this call
  const auto loop = cell->loop;
  loop->withEvents([&](Events& events) {
    auto& list = listOf<T>(events);
    const auto it = std::find_if(list.begin(), list.end(), [&](const Slot<T>& slot) { return slot.id == cell->id; });
    if (it != list.end()) {
      events.dead.emplace_back(std::move(*it));
      list.erase(it);
    }
  });
  loop->interrupt();
}

template void* addEvent<Io>(pa_mainloop_api*, Io);
template void* addEvent<Time>(pa_mainloop_api*, Time);
template void* addEvent<Defer>(pa_mainloop_api*, Defer);
template void changeEvent<Io>(void*, const std::function<void(Io&)>&);
template void changeEvent<Time>(void*, const std::function<void(Time&)>&);
template void changeEvent<Defer>(void*, const std::function<void(Defer&)>&);
template void freeEvent<Io>(void*);
template void freeEvent<Time>(void*);
template void freeEvent<Defer>(void*);

}  // namespace cadence::pulse
