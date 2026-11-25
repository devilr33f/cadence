#pragma once

#include "pulse-shim/abi.h"
#include "pulse-shim/events.h"

#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <thread>

namespace cadence::pulse {

using Job = std::function<void()>;

// one threaded mainloop, shared by its contexts, streams and events; a pa_threaded_mainloop is a handle on it.
//
// the mainloop lock is our own recursive per-thread lock (owner and depth under mutex_): callbacks into c run only
// on the loop thread (or the caller of a synchronous call) while it is held; other threads only post. the loop
// thread holds it except while it polls
class Loop : public std::enable_shared_from_this<Loop> {
 public:
  // null when its wake pipe cannot be made
  static std::shared_ptr<Loop> create();
  // the loop behind a table this library handed out, or behind a client's copy of one: a table is trusted only if
  // its userdata names a live loop and it has our functions, so anything else is refused without a read through it
  static std::shared_ptr<Loop> fromApi(const pa_mainloop_api* api);
  ~Loop();

  Loop(const Loop&) = delete;
  Loop& operator=(const Loop&) = delete;

  // runs job on the loop thread with the lock held; a job is dropped only when memory runs out
  void post(Job job) noexcept;
  // wakes every pa_threaded_mainloop_wait
  void signal();
  // ends the loop thread as a stop from it does
  void quit();
  // interrupts the loop thread's poll
  void interrupt() const;
  pa_mainloop_api* api() { return &api_; }

  void lock();
  void unlock();
  // releases the whole lock until a signal, then takes it back; a no-op on the loop thread or without the lock
  void wait();
  // false when its thread runs already or cannot start
  bool start();
  // from the loop thread it only asks the loop to end; elsewhere it joins it
  void stop();
  // destroys every event and drops the posted work: the loop is being freed
  void release();

  // f(events) under the state mutex; f must not call into c
  template <typename F>
  auto withEvents(F f) {
    std::scoped_lock lock(mutex_);
    return f(events_);
  }

 private:
  Loop() = default;

  void run();
  std::optional<Job> nextJob();
  [[nodiscard]] bool stopping();
  void drainWake() const;
  // a job that threw must not leave the lock held forever: the loop ends
  void abandon();

  std::mutex mutex_;
  std::condition_variable released_;
  std::condition_variable signalled_;
  // all of these under mutex_
  std::thread::id owner_;
  std::size_t depth_ = 0;
  std::uint64_t signals_ = 0;
  std::thread::id worker_;
  std::thread thread_;
  bool finished_ = false;
  bool stopping_ = false;
  std::deque<Job> jobs_;
  Events events_;

  int wakeRead_ = -1;
  int wakeWrite_ = -1;
  // written before the loop is shared, read by c afterwards
  pa_mainloop_api api_{};
};

// the vtable this library hands out, without its userdata, and whether a table has its functions
pa_mainloop_api vtable();
bool ourFunctions(const pa_mainloop_api& table);

}  // namespace cadence::pulse
