// the pa_mainloop_api vtable handed to clients; the events it makes live in events.cpp
#include "pulse-shim/abi.h"
#include "pulse-shim/events.h"
#include "pulse-shim/guard.h"
#include "pulse-shim/mainloop.h"

#include <optional>

namespace cadence::pulse {

namespace {

std::optional<timeval> copied(const timeval* tv) {
  if (tv == nullptr)
    return std::nullopt;
  return *tv;
}

pa_io_event* ioNew(pa_mainloop_api* a, int fd, pa_io_event_flags_t events, pa_io_event_cb_t cb, void* userdata) {
  return guarded(static_cast<pa_io_event*>(nullptr), [&] {
    return static_cast<pa_io_event*>(addEvent(a, Io{fd, events, cb, userdata, nullptr}));
  });
}

void ioEnable(pa_io_event* e, pa_io_event_flags_t events) {
  guarded([&] { changeEvent<Io>(e, [events](Io& io) { io.events = events; }); });
}

void ioFree(pa_io_event* e) {
  guarded([&] { freeEvent<Io>(e); });
}

void ioSetDestroy(pa_io_event* e, pa_io_event_destroy_cb_t cb) {
  guarded([&] { changeEvent<Io>(e, [cb](Io& io) { io.destroy = cb; }); });
}

pa_time_event* timeNew(pa_mainloop_api* a, const timeval* tv, pa_time_event_cb_t cb, void* userdata) {
  return guarded(static_cast<pa_time_event*>(nullptr), [&] {
    return static_cast<pa_time_event*>(addEvent(a, Time{copied(tv), cb, userdata, nullptr}));
  });
}

void timeRestart(pa_time_event* e, const timeval* tv) {
  guarded([&] {
    const auto at = copied(tv);
    changeEvent<Time>(e, [at](Time& time) { time.at = at; });
  });
}

void timeFree(pa_time_event* e) {
  guarded([&] { freeEvent<Time>(e); });
}

void timeSetDestroy(pa_time_event* e, pa_time_event_destroy_cb_t cb) {
  guarded([&] { changeEvent<Time>(e, [cb](Time& time) { time.destroy = cb; }); });
}

pa_defer_event* deferNew(pa_mainloop_api* a, pa_defer_event_cb_t cb, void* userdata) {
  return guarded(static_cast<pa_defer_event*>(nullptr), [&] {
    return static_cast<pa_defer_event*>(addEvent(a, Defer{true, cb, userdata, nullptr}));
  });
}

void deferEnable(pa_defer_event* e, int b) {
  guarded([&] { changeEvent<Defer>(e, [b](Defer& defer) { defer.enabled = b != 0; }); });
}

void deferFree(pa_defer_event* e) {
  guarded([&] { freeEvent<Defer>(e); });
}

void deferSetDestroy(pa_defer_event* e, pa_defer_event_destroy_cb_t cb) {
  guarded([&] { changeEvent<Defer>(e, [cb](Defer& defer) { defer.destroy = cb; }); });
}

// the threaded loop has no caller to return retval to: quitting ends the loop thread as a stop does
void quit(pa_mainloop_api* a, int) {
  guarded([&] {
    if (const auto loop = Loop::fromApi(a))
      loop->quit();
  });
}

}  // namespace

pa_mainloop_api vtable() {
  return {nullptr,  ioNew,    ioEnable,  ioFree,      ioSetDestroy, timeNew,         timeRestart, timeFree,
          timeSetDestroy, deferNew, deferEnable, deferFree, deferSetDestroy, quit};
}

// ours, or a client's copy of ours, which finds its loop through userdata as in libpulse. the registry already
// vouches for the loop userdata names, so this only refuses another library's table that names one
bool ourFunctions(const pa_mainloop_api& table) {
  const auto ours = vtable();
  return table.io_new == ours.io_new && table.io_enable == ours.io_enable && table.io_free == ours.io_free &&
         table.io_set_destroy == ours.io_set_destroy && table.time_new == ours.time_new &&
         table.time_restart == ours.time_restart && table.time_free == ours.time_free &&
         table.time_set_destroy == ours.time_set_destroy && table.defer_new == ours.defer_new &&
         table.defer_enable == ours.defer_enable && table.defer_free == ours.defer_free &&
         table.defer_set_destroy == ours.defer_set_destroy && table.quit == ours.quit;
}

}  // namespace cadence::pulse
