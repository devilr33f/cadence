#pragma once

#include "audiod/log.h"
#include "audiod/sys.h"
#include "result.h"

#include <atomic>
#include <chrono>
#include <cstdint>
#include <functional>
#include <map>
#include <memory>
#include <mutex>
#include <optional>
#include <unordered_map>
#include <vector>

struct epoll_event;

namespace cadence::audiod {

// the daemon's one event loop: fd watches (level-triggered epoll), timers by deadline and callbacks posted
// from other threads. every callback runs on the thread inside run(); post and stop are the only calls other
// threads may make
class Loop {
 public:
  using Clock = std::chrono::steady_clock;
  using IoCallback = std::function<void(std::uint32_t events)>;
  using Callback = std::function<void()>;
  using WatchId = std::uint64_t;
  using TimerId = std::uint64_t;

  // nullptr, logged, when epoll or its wake fd cannot be created
  static std::unique_ptr<Loop> create(const Log& log);
  ~Loop();

  Loop(const Loop&) = delete;
  Loop& operator=(const Loop&) = delete;

  // runs until stop; an error only when epoll itself failed
  std::optional<SysError> run();
  void stop();

  // events are epoll's (EPOLLIN, EPOLLRDHUP, ...); unwatch before closing the fd. a watch removed while
  // earlier events of the same batch run never sees its pending event
  Result<WatchId, SysError> watch(int fd, std::uint32_t events, IoCallback callback);
  void unwatch(WatchId id);

  // timers fire once, in deadline order and never early; a periodic one arms the next from its callback
  TimerId at(Clock::time_point deadline, Callback callback);
  TimerId after(Clock::duration delay, Callback callback);
  void cancel(TimerId id);

  // runs callback on the loop thread soon after
  void post(Callback callback);

 private:
  struct Watch {
    int fd;
    // shared, so a callback that unwatches itself is not destroyed while it runs
    std::shared_ptr<IoCallback> callback;
  };
  struct Timer {
    Clock::time_point deadline;
    Callback callback;
  };

  Loop(UniqueFd epoll, UniqueFd wake);

  void dispatch(const epoll_event& event);
  void runPosted();
  void fireDueTimers();
  [[nodiscard]] int waitMs() const;
  void wake() const;

  UniqueFd epoll_;
  UniqueFd wake_;
  std::atomic<bool> stopping_{false};
  WatchId nextWatch_ = 1;
  std::unordered_map<WatchId, Watch> watches_;
  TimerId nextTimer_ = 1;
  // by id, so equal deadlines fire in the order they were set
  std::map<TimerId, Timer> timers_;
  std::mutex postedMutex_;
  std::vector<Callback> posted_;
};

}  // namespace cadence::audiod
