#include "pulse-shim/mainloop.h"

#include "pulse-shim/guard.h"
#include "pulse-shim/util.h"

#include <fcntl.h>
#include <poll.h>
#include <pthread.h>
#include <unistd.h>

#include <array>
#include <map>
#include <utility>
#include <vector>

namespace cadence::pulse {

namespace {

// the loops alive, by the address their table carries in userdata. never freed: no destructor runs at exit while a
// loop thread may still look something up
struct Registry {
  std::mutex mutex;
  std::map<const void*, std::weak_ptr<Loop>> loops;
};

Registry& registry() {
  static auto* const loops = new Registry();
  return *loops;
}

// what a pa_threaded_mainloop points at
struct Handle {
  std::shared_ptr<Loop> loop;
};

Loop* handle(pa_threaded_mainloop* m) { return m == nullptr ? nullptr : reinterpret_cast<Handle*>(m)->loop.get(); }

}  // namespace

std::shared_ptr<Loop> Loop::create() {
  std::shared_ptr<Loop> loop(new Loop());
  std::array<int, 2> wake{};
  if (::pipe2(wake.data(), O_CLOEXEC | O_NONBLOCK) != 0)
    return nullptr;
  loop->wakeRead_ = wake[0];
  loop->wakeWrite_ = wake[1];
  loop->api_ = vtable();
  loop->api_.userdata = loop.get();
  auto& loops = registry();
  std::scoped_lock lock(loops.mutex);
  loops.loops[loop.get()] = loop;
  return loop;
}

Loop::~Loop() {
  {
    auto& loops = registry();
    std::scoped_lock lock(loops.mutex);
    loops.loops.erase(this);
  }
  // a loop thread that stopped itself and was never joined holds no reference any more
  if (thread_.joinable())
    thread_.detach();
  if (wakeRead_ >= 0)
    ::close(wakeRead_);
  if (wakeWrite_ >= 0)
    ::close(wakeWrite_);
}

std::shared_ptr<Loop> Loop::fromApi(const pa_mainloop_api* api) {
  if (api == nullptr)
    return nullptr;
  std::shared_ptr<Loop> found;
  {
    auto& loops = registry();
    std::scoped_lock lock(loops.mutex);
    if (const auto it = loops.loops.find(api->userdata); it != loops.loops.end())
      found = it->second.lock();
  }
  // a refused one may have been the last reference: it goes here, outside the registry's lock its destructor takes
  if (!found || !ourFunctions(*api))
    return nullptr;
  return found;
}

void Loop::post(Job job) noexcept {
  try {
    std::scoped_lock lock(mutex_);
    jobs_.push_back(std::move(job));
  } catch (...) {
    return;
  }
  interrupt();
}

void Loop::signal() {
  std::scoped_lock lock(mutex_);
  ++signals_;
  signalled_.notify_all();
}

void Loop::quit() {
  {
    std::scoped_lock lock(mutex_);
    stopping_ = true;
  }
  interrupt();
}

void Loop::interrupt() const {
  const char byte = 1;
  // a full pipe already wakes the poll
  const auto written = ::write(wakeWrite_, &byte, 1);
  (void)written;
}

void Loop::drainWake() const {
  std::array<char, 64> bytes{};
  while (::read(wakeRead_, bytes.data(), bytes.size()) > 0) {
  }
}

void Loop::lock() {
  const auto me = std::this_thread::get_id();
  std::unique_lock lock(mutex_);
  released_.wait(lock, [&] { return owner_ == std::thread::id() || owner_ == me; });
  owner_ = me;
  ++depth_;
}

void Loop::unlock() {
  std::scoped_lock lock(mutex_);
  if (owner_ != std::this_thread::get_id())
    return;
  if (--depth_ == 0) {
    owner_ = std::thread::id();
    released_.notify_all();
  }
}

void Loop::wait() {
  const auto me = std::this_thread::get_id();
  std::unique_lock lock(mutex_);
  if (owner_ != me || worker_ == me)
    return;
  const auto depth = std::exchange(depth_, 0);
  owner_ = std::thread::id();
  released_.notify_all();
  const auto seen = signals_;
  signalled_.wait(lock, [&] { return signals_ != seen; });
  released_.wait(lock, [&] { return owner_ == std::thread::id(); });
  owner_ = me;
  depth_ = depth;
}

bool Loop::start() {
  std::unique_lock lock(mutex_);
  // a loop that ended itself (the vtable's quit, a stop from a callback) left its finished thread behind
  if (thread_.joinable() && finished_) {
    auto ended = std::move(thread_);
    lock.unlock();
    ended.join();
    lock.lock();
  }
  if (thread_.joinable())
    return false;
  stopping_ = false;
  finished_ = false;
  try {
    thread_ = std::thread([self = shared_from_this()] {
      ::pthread_setname_np(::pthread_self(), "pulse-mainloop");
      // run catches what its jobs throw; nothing else may unwind out of the thread
      try {
        self->run();
      } catch (...) {
      }
    });
  } catch (...) {
    return false;
  }
  worker_ = thread_.get_id();
  return true;
}

void Loop::stop() {
  std::thread ending;
  {
    std::scoped_lock lock(mutex_);
    stopping_ = true;
    // the loop thread cannot join itself: its poll wakes to see the flag, and a start or a later stop reaps it
    if (worker_ == std::this_thread::get_id()) {
      interrupt();
      return;
    }
    ending = std::move(thread_);
  }
  interrupt();
  if (ending.joinable())
    ending.join();
  std::scoped_lock lock(mutex_);
  worker_ = std::thread::id();
}

void Loop::release() {
  std::vector<Dead> all;
  std::deque<Job> jobs;
  {
    std::scoped_lock lock(mutex_);
    all = events_.takeAll();
    jobs = std::exchange(jobs_, {});
  }
  for (auto& dead : all)
    destroy(dead, api());
}

std::optional<Job> Loop::nextJob() {
  std::scoped_lock lock(mutex_);
  if (jobs_.empty())
    return std::nullopt;
  auto job = std::move(jobs_.front());
  jobs_.pop_front();
  return job;
}

bool Loop::stopping() {
  std::scoped_lock lock(mutex_);
  return stopping_;
}

void Loop::abandon() {
  std::scoped_lock lock(mutex_);
  stopping_ = true;
  if (owner_ == std::this_thread::get_id()) {
    owner_ = std::thread::id();
    depth_ = 0;
  }
  ++signals_;
  released_.notify_all();
  signalled_.notify_all();
}

// one round: the posted work, then the vtable's events, then a poll with the lock released
void Loop::run() {
  lock();
  try {
    std::vector<std::pair<std::uint64_t, short>> ready;
    for (;;) {
      while (auto job = nextJob())
        (*job)();
      if (stopping())
        break;
      dispatch(*this, ready);
      const auto [watched, timeout] = withEvents([](const Events& events) { return events.pollSet(now()); });
      std::vector<pollfd> fds{{wakeRead_, POLLIN, 0}};
      for (const auto& entry : watched)
        fds.push_back(entry.fd);
      unlock();
      ::poll(fds.data(), fds.size(), timeout);
      lock();
      drainWake();
      ready.clear();
      for (std::size_t i = 0; i < watched.size(); ++i)
        if (fds[i + 1].revents != 0)
          ready.emplace_back(watched[i].id, fds[i + 1].revents);
    }
    unlock();
  } catch (...) {
    abandon();
  }
  std::scoped_lock lock(mutex_);
  finished_ = true;
}

}  // namespace cadence::pulse

using namespace cadence::pulse;

extern "C" {

pa_threaded_mainloop* pa_threaded_mainloop_new(void) {
  return guarded(static_cast<pa_threaded_mainloop*>(nullptr), []() -> pa_threaded_mainloop* {
    auto loop = Loop::create();
    if (!loop)
      return nullptr;
    return reinterpret_cast<pa_threaded_mainloop*>(new Handle{std::move(loop)});
  });
}

void pa_threaded_mainloop_free(pa_threaded_mainloop* m) {
  guarded([&] {
    if (m == nullptr)
      return;
    const std::unique_ptr<Handle> owned(reinterpret_cast<Handle*>(m));
    owned->loop->stop();
    owned->loop->release();
  });
}

int pa_threaded_mainloop_start(pa_threaded_mainloop* m) {
  return guarded(-1, [&] {
    auto* loop = handle(m);
    return loop != nullptr && loop->start() ? 0 : -1;
  });
}

void pa_threaded_mainloop_stop(pa_threaded_mainloop* m) {
  guarded([&] {
    if (auto* loop = handle(m))
      loop->stop();
  });
}

void pa_threaded_mainloop_lock(pa_threaded_mainloop* m) {
  guarded([&] {
    if (auto* loop = handle(m))
      loop->lock();
  });
}

void pa_threaded_mainloop_unlock(pa_threaded_mainloop* m) {
  guarded([&] {
    if (auto* loop = handle(m))
      loop->unlock();
  });
}

void pa_threaded_mainloop_wait(pa_threaded_mainloop* m) {
  guarded([&] {
    if (auto* loop = handle(m))
      loop->wait();
  });
}

// wait_for_accept is not honoured: pa_threaded_mainloop_accept is not part of this library
void pa_threaded_mainloop_signal(pa_threaded_mainloop* m, int) {
  guarded([&] {
    if (auto* loop = handle(m))
      loop->signal();
  });
}

pa_mainloop_api* pa_threaded_mainloop_get_api(pa_threaded_mainloop* m) {
  return guarded(static_cast<pa_mainloop_api*>(nullptr), [&]() -> pa_mainloop_api* {
    auto* loop = handle(m);
    return loop == nullptr ? nullptr : loop->api();
  });
}

}  // extern "C"
