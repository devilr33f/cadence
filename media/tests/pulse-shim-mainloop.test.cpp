// the shim's threaded mainloop and its api vtable, through the c api alone
#include "pulse-shim/abi.h"
#include "support/eventually.h"
#include "support/pulse-client.h"

#include <catch2/catch_test_macros.hpp>

#include <sys/syscall.h>
#include <unistd.h>

#include <atomic>
#include <chrono>
#include <filesystem>
#include <functional>
#include <future>
#include <mutex>
#include <string>
#include <thread>

using namespace cadence::test_support;
using namespace cadence::test_support::pulse;
using namespace std::chrono_literals;

namespace {

pa_threaded_mainloop* started() {
  auto* m = pa_threaded_mainloop_new();
  REQUIRE(m != nullptr);
  REQUIRE(pa_threaded_mainloop_start(m) == 0);
  return m;
}

void finish(pa_threaded_mainloop* m) {
  pa_threaded_mainloop_stop(m);
  pa_threaded_mainloop_free(m);
}

// what an event's callback saw
struct Event {
  std::atomic<int> fired{0};
  std::atomic<int> destroyed{0};
  std::mutex mutex;
  std::string thread;
  long task = 0;
  std::chrono::steady_clock::time_point at;
  int fd = -1;
  pa_io_event_flags_t flags = 0;
  // the defer's work, run before it frees itself
  std::function<void()> work;

  void record() {
    {
      std::scoped_lock lock(mutex);
      thread = threadName();
      task = ::syscall(SYS_gettid);
      at = std::chrono::steady_clock::now();
    }
    if (work)
      work();
    ++fired;
  }

  std::string threadSeen() {
    std::scoped_lock lock(mutex);
    return thread;
  }
};

Event& event(void* userdata) { return *static_cast<Event*>(userdata); }

void onDefer(pa_mainloop_api* a, pa_defer_event* e, void* userdata) {
  event(userdata).record();
  a->defer_free(e);
}

void onDeferDestroyed(pa_mainloop_api*, pa_defer_event*, void* userdata) { ++event(userdata).destroyed; }

void onTime(pa_mainloop_api* a, pa_time_event* e, const timeval*, void* userdata) {
  event(userdata).record();
  a->time_free(e);
}

void onTimeDestroyed(pa_mainloop_api*, pa_time_event*, void* userdata) { ++event(userdata).destroyed; }

void onIo(pa_mainloop_api* a, pa_io_event* e, int fd, pa_io_event_flags_t flags, void* userdata) {
  auto& seen = event(userdata);
  {
    std::scoped_lock lock(seen.mutex);
    seen.fd = fd;
    seen.flags = flags;
  }
  seen.record();
  char byte = 0;
  const auto got = ::read(fd, &byte, 1);
  (void)got;
  a->io_free(e);
}

void stopFromTime(pa_mainloop_api*, pa_time_event*, const timeval*, void* userdata) {
  pa_threaded_mainloop_stop(static_cast<pa_threaded_mainloop*>(userdata));
}

// a defer event that runs once, records and frees itself; made under the caller's lock
void defer(pa_threaded_mainloop* m, Event& seen) {
  auto* api = pa_threaded_mainloop_get_api(m);
  auto* e = api->defer_new(api, onDefer, &seen);
  REQUIRE(e != nullptr);
  api->defer_set_destroy(e, onDeferDestroyed);
}

// the loop thread's /proc task entry
std::string loopTask(pa_threaded_mainloop* m) {
  Event seen;
  pa_threaded_mainloop_lock(m);
  defer(m, seen);
  pa_threaded_mainloop_unlock(m);
  REQUIRE(eventually([&] { return seen.fired == 1; }));
  std::scoped_lock lock(seen.mutex);
  return "/proc/self/task/" + std::to_string(seen.task);
}

// runs f on another thread and fails, instead of hanging, when it does not return in time
template <typename F>
void bounded(F f) {
  auto done = std::async(std::launch::async, f);
  REQUIRE(done.wait_for(5s) == std::future_status::ready);
}

timeval in(std::chrono::microseconds delay) {
  timeval tv{};
  pa_gettimeofday(&tv);
  const auto usec = tv.tv_usec + delay.count();
  tv.tv_sec += static_cast<time_t>(usec / 1000000);
  tv.tv_usec = static_cast<suseconds_t>(usec % 1000000);
  return tv;
}

void quitForeign(pa_mainloop_api*, int) {}

void deferEnableForeign(pa_defer_event*, int) {}

}  // namespace

TEST_CASE("a defer event runs on the loop thread with the lock held") {
  auto* m = started();
  Event seen;
  std::atomic<bool> entered{false};
  std::atomic<bool> done{false};
  seen.work = [&] {
    entered = true;
    std::this_thread::sleep_for(100ms);
    done = true;
  };
  pa_threaded_mainloop_lock(m);
  defer(m, seen);
  pa_threaded_mainloop_unlock(m);
  REQUIRE(eventually([&] { return entered.load(); }));
  pa_threaded_mainloop_lock(m);
  CHECK(done);
  pa_threaded_mainloop_unlock(m);
  CHECK(seen.threadSeen() == kLoopThread);
  REQUIRE(eventually([&] { return seen.destroyed == 1; }));
  finish(m);
}

TEST_CASE("wait releases the lock until a signal") {
  auto* m = started();
  Event seen;
  std::atomic<bool> done{false};
  seen.work = [&] {
    done = true;
    pa_threaded_mainloop_signal(m, 0);
  };
  pa_threaded_mainloop_lock(m);
  defer(m, seen);
  int waits = 0;
  while (!done) {
    pa_threaded_mainloop_wait(m);
    ++waits;
  }
  pa_threaded_mainloop_unlock(m);
  CHECK(waits == 1);
  finish(m);
}

TEST_CASE("the lock is recursive") {
  auto* m = started();
  Event seen;
  pa_threaded_mainloop_lock(m);
  pa_threaded_mainloop_lock(m);
  pa_threaded_mainloop_unlock(m);
  defer(m, seen);
  std::this_thread::sleep_for(50ms);
  CHECK(seen.fired == 0);
  pa_threaded_mainloop_unlock(m);
  CHECK(eventually([&] { return seen.fired == 1; }));
  finish(m);
}

TEST_CASE("stop then free joins the loop thread") {
  auto* m = started();
  const auto task = loopTask(m);
  CHECK(std::filesystem::exists(task));
  finish(m);
  // the kernel drops the task entry a moment after pthread_join returns
  CHECK(eventually([&] { return !std::filesystem::exists(task); }));
}

// a time event, not a defer: an enabled defer wakes the poll by itself and would hide a stop that forgets to
TEST_CASE("a stop from a callback ends the loop, which starts again, and free still completes") {
  auto* m = started();
  const auto task = loopTask(m);
  auto* api = pa_threaded_mainloop_get_api(m);
  const auto now = in(0us);
  pa_threaded_mainloop_lock(m);
  REQUIRE(api->time_new(api, &now, stopFromTime, m) != nullptr);
  pa_threaded_mainloop_unlock(m);
  CHECK(eventually([&] { return !std::filesystem::exists(task); }));
  REQUIRE(pa_threaded_mainloop_start(m) == 0);
  bounded([m] { finish(m); });
}

TEST_CASE("the api vtable runs a time event when due and a defer event, each once, then destroys them") {
  auto* m = started();
  auto* api = pa_threaded_mainloop_get_api(m);
  Event timer;
  Event deferred;
  const auto begin = std::chrono::steady_clock::now();
  const auto at = in(10ms);
  pa_threaded_mainloop_lock(m);
  auto* t = api->time_new(api, &at, onTime, &timer);
  REQUIRE(t != nullptr);
  api->time_set_destroy(t, onTimeDestroyed);
  defer(m, deferred);
  pa_threaded_mainloop_unlock(m);
  REQUIRE(eventually([&] { return timer.destroyed == 1 && deferred.destroyed == 1; }));
  std::this_thread::sleep_for(50ms);
  for (auto* seen : {&timer, &deferred}) {
    CHECK(seen->fired == 1);
    CHECK(seen->destroyed == 1);
    CHECK(seen->threadSeen() == kLoopThread);
  }
  std::scoped_lock lock(timer.mutex);
  CHECK(timer.at - begin >= 9ms);
  finish(m);
}

TEST_CASE("an io event fires on the loop thread when its fd is readable") {
  auto* m = started();
  auto* api = pa_threaded_mainloop_get_api(m);
  int fds[2] = {-1, -1};
  REQUIRE(::pipe(fds) == 0);
  Event io;
  pa_threaded_mainloop_lock(m);
  REQUIRE(api->io_new(api, fds[0], PA_IO_EVENT_INPUT, onIo, &io) != nullptr);
  pa_threaded_mainloop_unlock(m);
  std::this_thread::sleep_for(30ms);
  CHECK(io.fired == 0);
  REQUIRE(::write(fds[1], "x", 1) == 1);
  REQUIRE(eventually([&] { return io.fired == 1; }));
  std::this_thread::sleep_for(30ms);
  CHECK(io.fired == 1);
  {
    std::scoped_lock lock(io.mutex);
    CHECK(io.fd == fds[0]);
    CHECK((io.flags & PA_IO_EVENT_INPUT) != 0);
  }
  CHECK(io.threadSeen() == kLoopThread);
  finish(m);
  ::close(fds[0]);
  ::close(fds[1]);
}

TEST_CASE("a context takes the loop's table or a client's copy of it, never another library's") {
  auto* m = started();
  auto* ours = pa_threaded_mainloop_get_api(m);
  const auto made = [&](pa_mainloop_api* api) {
    pa_threaded_mainloop_lock(m);
    auto* c = pa_context_new(api, "test");
    if (c != nullptr)
      pa_context_unref(c);
    pa_threaded_mainloop_unlock(m);
    return c != nullptr;
  };
  CHECK(made(ours));
  CHECK_FALSE(made(nullptr));

  // a client may copy the table: the loop is found through userdata, as in libpulse
  auto copy = *ours;
  CHECK(made(&copy));

  // our userdata on another library's functions
  auto foreign = *ours;
  foreign.quit = quitForeign;
  CHECK_FALSE(made(&foreign));
  foreign = *ours;
  foreign.defer_enable = deferEnableForeign;
  CHECK_FALSE(made(&foreign));
  foreign = *ours;
  foreign.io_new = nullptr;
  CHECK_FALSE(made(&foreign));

  // our functions on a userdata that is no loop of ours: never read through
  long elsewhere = 0;
  auto stray = *ours;
  stray.userdata = &elsewhere;
  CHECK_FALSE(made(&stray));
  stray.userdata = nullptr;
  CHECK_FALSE(made(&stray));

  // a copy outlives its loop as a table, not as a way to it
  finish(m);
  CHECK(pa_context_new(&copy, "test") == nullptr);
}
